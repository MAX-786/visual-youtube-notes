package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCookiesArgsCopyTheOperatorsJarPerRun(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "youtube_cookies")
	y := &ytdlp{cacheDir: dir, cookies: secret}

	// Nothing configured, a missing file and an empty file all mean no cookies.
	if args, cleanup := (&ytdlp{cacheDir: dir}).cookiesArgs(); args != nil {
		cleanup()
		t.Fatalf("no cookies configured gave %v", args)
	}
	if args, cleanup := y.cookiesArgs(); args != nil {
		cleanup()
		t.Fatalf("a missing file gave %v", args)
	}
	if err := os.WriteFile(secret, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if args, cleanup := y.cookiesArgs(); args != nil {
		cleanup()
		t.Fatalf("an empty file gave %v", args)
	}

	jar := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tvalue\n"
	if err := os.WriteFile(secret, []byte(jar), 0o600); err != nil {
		t.Fatal(err)
	}
	args, cleanup := y.cookiesArgs()
	if len(args) != 2 || args[0] != "--cookies" {
		t.Fatalf("args %v", args)
	}
	copyPath := args[1]
	if copyPath == secret || filepath.Dir(copyPath) != dir || !strings.HasPrefix(filepath.Base(copyPath), "cookies-") {
		t.Fatalf("the copy should be a private file in the cache dir, got %s", copyPath)
	}
	if data, err := os.ReadFile(copyPath); err != nil || string(data) != jar {
		t.Fatalf("copy content %q %v", data, err)
	}
	// Two concurrent runs get two jars; cleanup removes only its own.
	again, cleanupAgain := y.cookiesArgs()
	if again[1] == copyPath {
		t.Fatal("two runs shared one jar")
	}
	cleanup()
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatal("cleanup left the first jar behind")
	}
	if _, err := os.Stat(again[1]); err != nil {
		t.Fatal("cleanup removed the other run's jar")
	}
	cleanupAgain()
	if data, err := os.ReadFile(secret); err != nil || string(data) != jar {
		t.Fatal("the secret itself was touched")
	}
}
