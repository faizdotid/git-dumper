package main

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

type fixtureObjects struct {
	blob, tree, commit plumbing.Hash
}

// buildFixture creates a blob, a tree and a commit in the given storage.
func buildFixture(t *testing.T, st *memory.Storage) fixtureObjects {
	t.Helper()

	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blobWriter, err := blob.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobWriter.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := blobWriter.Close(); err != nil {
		t.Fatal(err)
	}
	blobHash, err := st.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}

	tree := &object.Tree{
		Entries: []object.TreeEntry{{Name: "hello.txt", Mode: 0o100644, Hash: blobHash}},
	}
	treeObj := st.NewEncodedObject()
	if err := tree.Encode(treeObj); err != nil {
		t.Fatal(err)
	}
	treeHash, err := st.SetEncodedObject(treeObj)
	if err != nil {
		t.Fatal(err)
	}

	sig := object.Signature{Name: "t", Email: "t@example.com", When: time.Unix(0, 0)}
	commit := &object.Commit{
		Author:    sig,
		Committer: sig,
		Message:   "init\n",
		TreeHash:  treeHash,
	}
	commitObj := st.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		t.Fatal(err)
	}
	commitHash, err := st.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatal(err)
	}

	return fixtureObjects{blob: blobHash, tree: treeHash, commit: commitHash}
}

// writeLooseObject writes an object in the loose format
// (.git/objects/xx/yyyy...) under root.
func writeLooseObject(t *testing.T, root string, h plumbing.Hash, obj plumbing.EncodedObject) string {
	t.Helper()

	dir := filepath.Join(root, "objects", h.String()[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, h.String()[2:])
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := objfile.NewWriter(f)
	defer w.Close()

	if err := w.WriteHeader(obj.Type(), obj.Size()); err != nil {
		t.Fatal(err)
	}
	reader, err := obj.Reader()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := io.Copy(w, reader); err != nil {
		t.Fatal(err)
	}
	return path
}

func sortedHashes(hashes []string) []string {
	sort.Strings(hashes)
	return hashes
}

func TestGetReferencedSHA1(t *testing.T) {
	st := memory.NewStorage()
	objs := buildFixture(t, st)
	root := t.TempDir()

	commitObj, err := st.EncodedObject(plumbing.AnyObject, objs.commit)
	if err != nil {
		t.Fatal(err)
	}
	commitPath := writeLooseObject(t, root, objs.commit, commitObj)

	refs, err := getReferencedSHA1(commitPath)
	if err != nil {
		t.Fatalf("getReferencedSHA1(commit): %s", err)
	}
	if len(refs) != 1 || refs[0] != objs.tree.String() {
		t.Errorf("commit refs = %v, want [%s]", refs, objs.tree)
	}

	treeObj, err := st.EncodedObject(plumbing.AnyObject, objs.tree)
	if err != nil {
		t.Fatal(err)
	}
	treePath := writeLooseObject(t, root, objs.tree, treeObj)

	refs, err = getReferencedSHA1(treePath)
	if err != nil {
		t.Fatalf("getReferencedSHA1(tree): %s", err)
	}
	if len(refs) != 1 || refs[0] != objs.blob.String() {
		t.Errorf("tree refs = %v, want [%s]", refs, objs.blob)
	}

	blobObj, err := st.EncodedObject(plumbing.AnyObject, objs.blob)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := writeLooseObject(t, root, objs.blob, blobObj)

	refs, err = getReferencedSHA1(blobPath)
	if err != nil {
		t.Fatalf("getReferencedSHA1(blob): %s", err)
	}
	if len(refs) != 0 {
		t.Errorf("blob refs = %v, want none", refs)
	}
}

func TestGetReferencedSHA1Garbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage")
	if err := os.WriteFile(path, []byte("not a zlib stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := getReferencedSHA1(path); err == nil {
		t.Error("expected an error for a garbage object file")
	}
}

func TestIndexObjects(t *testing.T) {
	st := memory.NewStorage()
	objs := buildFixture(t, st)

	idx := &index.Index{
		Version: 2,
		Entries: []*index.Entry{{
			Name:       "hello.txt",
			Hash:       objs.blob,
			Mode:       0o100644,
			Size:       6,
			CreatedAt:  time.Unix(0, 0),
			ModifiedAt: time.Unix(0, 0),
		}},
	}
	path := filepath.Join(t.TempDir(), "index")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.NewEncoder(f).Encode(idx); err != nil {
		t.Fatal(err)
	}
	f.Close()

	entries, err := indexObjects(path)
	if err != nil {
		t.Fatalf("indexObjects: %s", err)
	}
	if len(entries) != 1 || entries[0] != objs.blob.String() {
		t.Errorf("indexObjects = %v, want [%s]", entries, objs.blob)
	}
}

func TestIndexObjectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(path, []byte("DIRC garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := indexObjects(path); err == nil {
		t.Error("expected an error for a garbage index")
	}
}

// writeFixturePack writes a pack containing the fixture objects and its
// matching .idx file, returning both paths.
func writeFixturePack(t *testing.T, st *memory.Storage, objs fixtureObjects, dir string) (string, string) {
	t.Helper()

	packPath := filepath.Join(dir, "pack-test.pack")
	packFile, err := os.Create(packPath)
	if err != nil {
		t.Fatal(err)
	}
	encoder := packfile.NewEncoder(packFile, st, false)
	if _, err := encoder.Encode([]plumbing.Hash{objs.commit, objs.tree, objs.blob}, 10); err != nil {
		packFile.Close()
		t.Fatalf("pack encode: %s", err)
	}
	packFile.Close()

	// Build the .idx by scanning the pack again.
	scanFile, err := os.Open(packPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := &idxfile.Writer{}
	parser, err := packfile.NewParser(packfile.NewScanner(scanFile), writer)
	if err != nil {
		scanFile.Close()
		t.Fatal(err)
	}
	if _, err := parser.Parse(); err != nil {
		scanFile.Close()
		t.Fatalf("pack parse: %s", err)
	}
	scanFile.Close()

	memIdx, err := writer.Index()
	if err != nil {
		t.Fatal(err)
	}

	idxPath := filepath.Join(dir, "pack-test.idx")
	idxFile, err := os.Create(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idxfile.NewEncoder(idxFile).Encode(memIdx); err != nil {
		idxFile.Close()
		t.Fatalf("idx encode: %s", err)
	}
	idxFile.Close()

	return packPath, idxPath
}

func TestPackObjects(t *testing.T) {
	st := memory.NewStorage()
	objs := buildFixture(t, st)
	dir := t.TempDir()
	packPath, idxPath := writeFixturePack(t, st, objs, dir)

	packed, referenced, err := packObjects(packPath, idxPath)
	if err != nil {
		t.Fatalf("packObjects: %s", err)
	}

	wantPacked := sortedHashes([]string{objs.blob.String(), objs.tree.String(), objs.commit.String()})
	gotPacked := sortedHashes(packed)
	if len(gotPacked) != len(wantPacked) {
		t.Fatalf("packed objects = %v, want %v", gotPacked, wantPacked)
	}
	for i := range wantPacked {
		if gotPacked[i] != wantPacked[i] {
			t.Errorf("packed[%d] = %s, want %s", i, gotPacked[i], wantPacked[i])
		}
	}

	// The commit references the tree and the tree references the blob.
	// Blobs must not be read/decompressed, so they contribute nothing.
	wantRefs := map[string]bool{objs.tree.String(): true, objs.blob.String(): true}
	if len(referenced) != len(wantRefs) {
		t.Fatalf("referenced objects = %v, want %v", referenced, wantRefs)
	}
	for _, ref := range referenced {
		if !wantRefs[ref] {
			t.Errorf("unexpected referenced object %s", ref)
		}
	}
}

func TestPackObjectsRelativePath(t *testing.T) {
	st := memory.NewStorage()
	objs := buildFixture(t, st)
	dir := t.TempDir()
	writeFixturePack(t, st, objs, dir)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	// Relative paths must work as well (Windows portability fix).
	packed, _, err := packObjects("pack-test.pack", "pack-test.idx")
	if err != nil {
		t.Fatalf("packObjects with relative paths: %s", err)
	}
	if len(packed) != 3 {
		t.Errorf("packed objects = %v, want 3", packed)
	}
}

func TestGetReferencedSHA1UnexpectedType(t *testing.T) {
	// An "unexpected type" is reported as an error and must not panic.
	if _, err := referencedSHA1s(plumbing.InvalidObject, nil); err == nil {
		t.Error("expected an error for an invalid object type")
	}
}
