package play

import (
	"bytes"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/cryptofyre/transarc/internal/config"
	_ "modernc.org/sqlite"
)

// FillTokens reads a Plex or Jellyfin secret from the server's own files
// when the config did not already carry one. Call this before dropping
// privileges. The returned notes are safe to log: they do not contain the secret.
func FillTokens(cfg *config.Config) []string {
	var notes []string
	if note := fillToken("plex", &cfg.Plex, tokenFromPlex); note != "" {
		notes = append(notes, note)
	}
	if note := fillToken("jellyfin", &cfg.Jellyfin, tokenFromJellyfin); note != "" {
		notes = append(notes, note)
	}
	return notes
}

func fillToken(kind string, p *config.Play, read func(string) (string, error)) string {
	if p.Secret() != "" || strings.TrimSpace(p.Preferences) == "" {
		return ""
	}
	tok, err := read(p.Preferences)
	if err != nil {
		p.TokenErr = err.Error()
		return err.Error()
	}
	if kind == "jellyfin" {
		p.APIKey = tok
	} else {
		p.Token = tok
	}
	return ""
}

func tokenFromPlex(path string) (string, error) {
	file, err := plexPrefs(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("plex preferences: %w", err)
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	var doc struct {
		Token string `xml:"PlexOnlineToken,attr"`
	}
	// A syntax error from this file is not wrapped. Preferences.xml is one
	// line, and the error text would include the token.
	if xml.Unmarshal(b, &doc) != nil {
		return "", errors.New("plex preferences: could not read PlexOnlineToken")
	}
	tok := strings.TrimSpace(doc.Token)
	if tok == "" {
		return "", errors.New("plex preferences: PlexOnlineToken is empty")
	}
	return tok, nil
}

func plexPrefs(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("plex preferences: %w", err)
	}
	if !fi.IsDir() {
		return path, nil
	}
	for _, rel := range []string{
		"Preferences.xml",
		filepath.Join("Plex Media Server", "Preferences.xml"),
	} {
		p := filepath.Join(path, rel)
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("plex preferences: Preferences.xml not found")
}

func tokenFromJellyfin(path string) (string, error) {
	dbPath, err := jellyfinDB(path)
	if err != nil {
		return "", err
	}
	db, err := sql.Open("sqlite", sqliteURI(dbPath, true))
	if err != nil {
		return "", fmt.Errorf("jellyfin database: %w", err)
	}
	defer db.Close()
	tok, err := jellyfinAccessToken(db)
	if err != nil {
		return "", err
	}
	return tok, nil
}

func jellyfinDB(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("jellyfin config: %w", err)
	}
	if !fi.IsDir() {
		return path, nil
	}
	for _, rel := range []string{"data/jellyfin.db", "jellyfin.db"} {
		p := filepath.Join(path, filepath.FromSlash(rel))
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("jellyfin config: jellyfin.db not found")
}

func jellyfinAccessToken(db *sql.DB) (string, error) {
	cols, err := columnNames(db, "ApiKeys")
	if err != nil {
		return "", err
	}
	tokenCol := pickColumn(cols, "AccessToken")
	if tokenCol == "" {
		return "", errors.New("jellyfin database: ApiKeys has no access token column")
	}
	tokenIdent, ok := quoteIdent(tokenCol)
	if !ok {
		return "", errors.New("jellyfin database: access token column was rejected")
	}
	order := ""
	var parts []string
	if name := pickColumn(cols, "Name"); name != "" {
		if id, ok := quoteIdent(name); ok {
			parts = append(parts, "CASE WHEN lower(coalesce("+id+", '')) = 'transarc' THEN 0 ELSE 1 END")
		}
	}
	if date := pickColumn(cols, "DateLastActivity"); date != "" {
		if id, ok := quoteIdent(date); ok {
			parts = append(parts, id+" DESC")
		}
	}
	if len(parts) > 0 {
		order = " ORDER BY " + strings.Join(parts, ", ")
	}
	q := "SELECT " + tokenIdent + " FROM ApiKeys WHERE " + tokenIdent + " IS NOT NULL AND " + tokenIdent + " != ''" + order + " LIMIT 1"
	var tok string
	switch err := db.QueryRow(q).Scan(&tok); {
	case errors.Is(err, sql.ErrNoRows):
		return "", errors.New("jellyfin database: no API key")
	case err != nil:
		return "", errors.New("jellyfin database: could not read an API key")
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", errors.New("jellyfin database: no API key")
	}
	return tok, nil
}

func columnNames(db *sql.DB, table string) (map[string]string, error) {
	if _, ok := quoteIdent(table); !ok {
		return nil, errors.New("jellyfin database: table name was rejected")
	}
	rows, err := db.Query("SELECT name FROM pragma_table_info('" + table + "')")
	if err != nil {
		return nil, errors.New("jellyfin database: could not read ApiKeys")
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, errors.New("jellyfin database: could not read ApiKeys")
		}
		out[strings.ToLower(name)] = name
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("jellyfin database: could not read ApiKeys")
	}
	if len(out) == 0 {
		return nil, errors.New("jellyfin database: ApiKeys table not found")
	}
	return out, nil
}

func pickColumn(cols map[string]string, want string) string {
	if cols == nil {
		return ""
	}
	return cols[strings.ToLower(want)]
}

func quoteIdent(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	for _, r := range name {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", false
		}
	}
	return `"` + name + `"`, true
}

func sqliteURI(path string, ro bool) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	if ro {
		u.RawQuery = "mode=ro"
	}
	return u.String()
}
