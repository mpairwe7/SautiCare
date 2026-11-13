package devkeys

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sautihealth.com/sauti/internal/crypto"
)

func generated(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := Generate(dir, rand.Reader); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return dir
}

func TestDevkeysRefuseProduction(t *testing.T) {
	dir := generated(t)
	if _, err := Open(dir, "production"); !errors.Is(err, ErrProductionForbidden) {
		t.Fatalf("Open(production) = %v, want ErrProductionForbidden", err)
	}
}

func TestDevkeysFileIsPrivate(t *testing.T) {
	dir := generated(t)
	fi, err := os.Stat(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("keys.json mode %v is readable by group/others", fi.Mode().Perm())
	}
	if err := os.Chmod(filepath.Join(dir, "keys.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, "dev"); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("Open with 0644 keys = %v, want ErrInsecurePermissions", err)
	}
}

func TestDevkeysProvideCurrentPreviousAndSigner(t *testing.T) {
	st, err := Open(generated(t), "dev")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cur, err := st.Current(ctx, crypto.ClassIdentifiers)
	if err != nil || len(cur.Key) != 32 {
		t.Fatalf("Current = %v, %v", cur.ID, err)
	}
	prev, ok, err := st.Previous(ctx, crypto.ClassIdentifiers)
	if err != nil || !ok || prev.ID == cur.ID {
		t.Fatalf("Previous = %v %v %v", prev.ID, ok, err)
	}
	if got, err := st.ByID(ctx, prev.ID); err != nil || got.ID != prev.ID {
		t.Fatalf("ByID(prev) = %v, %v", got.ID, err)
	}
	if _, err := st.ByID(ctx, "nope"); !errors.Is(err, crypto.ErrUnknownKey) {
		t.Fatalf("ByID(nope) = %v", err)
	}
	s := st.Signer()
	sig, err := s.Sign(ctx, []byte("m"))
	if err != nil {
		t.Fatal(err)
	}
	if err := crypto.VerifyMLDSA65(s.PublicKey(), []byte("m"), sig); err != nil {
		t.Fatalf("signer output does not verify: %v", err)
	}
	if s.KeyID() == "" {
		t.Fatal("empty signer key id")
	}
}

func TestWitnessIsWriteOnce(t *testing.T) {
	w := NewFSWitness(t.TempDir())
	ctx := context.Background()
	if err := w.Put(ctx, "checkpoint-1.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := w.Put(ctx, "checkpoint-1.txt", []byte("b")); err == nil {
		t.Fatal("second Put of the same name succeeded")
	}
	if err := w.Put(ctx, "../escape.txt", []byte("x")); err == nil {
		t.Fatal("path traversal accepted")
	}
}

// Development keys open only in an explicit dev or test environment; any
// other value (including a mis-cased "Production") is refused (M7).
func TestDevkeysRefuseNonDevEnvironments(t *testing.T) {
	dir := generated(t)
	for _, env := range []string{"Production", "prod", "", "staging"} {
		if _, err := Open(dir, env); !errors.Is(err, ErrProductionForbidden) {
			t.Errorf("Open(%q) = %v, want ErrProductionForbidden", env, err)
		}
	}
	if _, err := Open(dir, "test"); err != nil {
		t.Errorf("Open(test) = %v", err)
	}
}

// CodeRabbit PR #2: regenerating must never replace existing key material —
// ciphertexts and checkpoint signatures would no longer verify.
func TestGenerateNeverOverwritesKeys(t *testing.T) {
	dir := generated(t)
	before, _ := os.ReadFile(filepath.Join(dir, "keys.json"))
	if err := Generate(dir, rand.Reader); err == nil {
		t.Fatal("second Generate in the same directory succeeded")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "keys.json"))
	if string(before) != string(after) {
		t.Fatal("existing keys.json was modified")
	}
}

func TestOpenRejectsMalformedKeyLengths(t *testing.T) {
	dir := generated(t)
	p := filepath.Join(dir, "keys.json")
	b, _ := os.ReadFile(p)
	var kf map[string]any
	if err := json.Unmarshal(b, &kf); err != nil {
		t.Fatal(err)
	}
	classes := kf["classes"].(map[string]any)
	for _, v := range classes {
		v.(map[string]any)["current"].(map[string]any)["key"] = "AAEC" // 3 bytes
	}
	b, _ = json.Marshal(kf)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, "dev"); err == nil {
		t.Fatal("Open accepted a 3-byte data key")
	}
}

// A witness note becomes visible only complete: nothing partial, nothing
// temporary is ever returned by ReadAll (CodeRabbit PR #2).
func TestWitnessPublishesOnlyCompleteNotes(t *testing.T) {
	dir := t.TempDir()
	w := NewFSWitness(dir)
	ctx := context.Background()
	if err := w.Put(ctx, "checkpoint-1-1.txt", []byte("note")); err != nil {
		t.Fatal(err)
	}
	if err := w.Put(ctx, "checkpoint-1-1.txt", []byte("other")); err == nil {
		t.Fatal("existing note replaced")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want only the published note (no temporary files)", names)
	}
	// A leftover temporary file from a crash is never read as a note.
	if err := os.WriteFile(filepath.Join(dir, ".tmp-checkpoint-2-2.txt"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	notes, err := w.ReadAll()
	if err != nil || len(notes) != 1 || string(notes[0]) != "note" {
		t.Fatalf("ReadAll = %q, %v", notes, err)
	}
}
