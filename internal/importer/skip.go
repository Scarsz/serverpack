package importer

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// runtimeDirs are server-generated directories that never belong in Git.
var runtimeDirs = map[string]bool{
	"logs": true, "cache": true, "crash-reports": true, "libraries": true, "versions": true,
	"debug": true, ".fabric": true, ".git": true, ".serverpack": true, "config/.paper-remapped": true,
	"plugins/.paper-remapped": true, "plugins/bStats/temp": true, "bundler": true,
}

// runtimeFiles are server-generated files that never belong in Git.
var runtimeFiles = map[string]bool{
	"usercache.json": true, "version_history.json": true, ".console_history": true,
	"session.lock": true, "server.jar.old": true,
}

// IsRuntimeDir reports whether rel (a slash path) is a generated directory.
func IsRuntimeDir(rel string) bool {
	return runtimeDirs[rel]
}

// IsRuntimeFile reports whether rel (a slash path) is a generated file.
func IsRuntimeFile(rel string) bool {
	return runtimeFiles[rel] || runtimeFiles[path.Base(rel)] && !strings.Contains(rel, "/")
}

// IsWorldDir reports whether dir contains a Minecraft world.
func IsWorldDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "level.dat"))
	return err == nil
}

// isDatabase reports whether a file looks like an embedded database (runtime state).
func isDatabase(rel string) bool {
	for _, ext := range []string{".db", ".sqlite", ".sqlite3", ".mv.db", ".h2.db", ".trace.db", ".db-journal", ".db-wal", ".db-shm"} {
		if strings.HasSuffix(strings.ToLower(rel), ext) {
			return true
		}
	}
	return false
}
