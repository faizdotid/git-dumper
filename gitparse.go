package main

import (
	"io"
	"os"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// decodeObject wraps raw object content in an EncodedObject and decodes it.
func decodeObject(typ plumbing.ObjectType, content []byte) (plumbing.EncodedObject, error) {
	obj := &plumbing.MemoryObject{}
	obj.SetType(typ)
	obj.SetSize(int64(len(content)))
	if _, err := obj.Write(content); err != nil {
		return nil, err
	}
	return obj, nil
}

// referencedSHA1s returns all the SHA1s referenced by the given object.
// Mirrors the Python get_referenced_sha1(): commits reference their tree and
// parents, trees reference their entries, blobs and tags reference nothing.
func referencedSHA1s(typ plumbing.ObjectType, content []byte) ([]string, error) {
	obj, err := decodeObject(typ, content)
	if err != nil {
		return nil, err
	}

	var objs []string
	switch typ {
	case plumbing.CommitObject:
		commit := new(object.Commit)
		if err := commit.Decode(obj); err != nil {
			return nil, err
		}
		objs = append(objs, commit.TreeHash.String())
		for _, parent := range commit.ParentHashes {
			objs = append(objs, parent.String())
		}
	case plumbing.TreeObject:
		tree := new(object.Tree)
		if err := tree.Decode(obj); err != nil {
			return nil, err
		}
		for _, entry := range tree.Entries {
			objs = append(objs, entry.Hash.String())
		}
	case plumbing.BlobObject, plumbing.TagObject:
		// no references
	default:
		return nil, &unexpectedTypeError{typ}
	}
	return objs, nil
}

type unexpectedTypeError struct{ typ plumbing.ObjectType }

func (e *unexpectedTypeError) Error() string {
	return "unexpected object type: " + e.typ.String()
}

// getReferencedSHA1 parses a loose object file and returns referenced SHA1s.
func getReferencedSHA1(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader, err := objfile.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	header, _, err := reader.Header()
	if err != nil {
		return nil, err
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	return referencedSHA1s(header, content)
}

// indexObjects returns the SHA1 of every blob in a .git/index file.
func indexObjects(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var idx index.Index
	if err := index.NewDecoder(f).Decode(&idx); err != nil {
		return nil, err
	}

	var objs []string
	for _, entry := range idx.Entries {
		objs = append(objs, entry.Hash.String())
	}
	return objs, nil
}

// packObjects returns the SHA1 of every object in a pack (packedObjs) and the
// SHA1s those objects reference (referencedObjs).
func packObjects(packPath, idxPath string) (packedObjs, referencedObjs []string, err error) {
	idxFile, err := os.Open(idxPath)
	if err != nil {
		return nil, nil, err
	}
	defer idxFile.Close()

	idx := idxfile.NewMemoryIndex()
	if err := idxfile.NewDecoder(idxFile).Decode(idx); err != nil {
		return nil, nil, err
	}

	fs := osfs.New("/")
	file, err := fs.Open(packPath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	pack := packfile.NewPackfile(idx, fs, file, 0)

	entries, err := idx.Entries()
	if err != nil {
		return nil, nil, err
	}
	defer entries.Close()

	for {
		entry, err := entries.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return packedObjs, referencedObjs, err
		}

		obj, err := pack.Get(entry.Hash)
		if err != nil {
			return packedObjs, referencedObjs, err
		}

		reader, err := obj.Reader()
		if err != nil {
			return packedObjs, referencedObjs, err
		}
		content, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return packedObjs, referencedObjs, err
		}

		packedObjs = append(packedObjs, entry.Hash.String())
		refs, err := referencedSHA1s(obj.Type(), content)
		if err != nil {
			// unknown object type: skip instead of aborting the dump
			continue
		}
		referencedObjs = append(referencedObjs, refs...)
	}

	return packedObjs, referencedObjs, nil
}
