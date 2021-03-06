package git

import (
	"sort"
)

// Options that are shared between git diff, git diff-files, diff-index,
// and diff-tree
type DiffCommonOptions struct {
	// Print a patch, not just the sha differences
	Patch bool

	// The 0 value implies 3.
	NumContextLines int

	// Generate the diff in raw format, not a unified diff
	Raw bool
}

// Describes the options that may be specified on the command line for
// "git diff-files". Note that only raw mode is currently supported, even
// though all the other options are parsed/set in this struct.
type DiffFilesOptions struct {
	DiffCommonOptions
}

// DiffFiles implements the git diff-files command.
// It compares the file system to the index.
func DiffFiles(c *Client, opt DiffFilesOptions, paths []string) ([]HashDiff, error) {
	indexentries, err := LsFiles(
		c,
		&LsFilesOptions{
			Cached: true, Deleted: true, Modified: true,
		},
		paths,
	)
	if err != nil {
		return nil, err
	}

	var val []HashDiff

	for _, idx := range indexentries {
		fs := TreeEntry{}
		idxtree := TreeEntry{idx.Sha1, idx.Mode}

		f, err := idx.PathName.FilePath(c)
		if err != nil || !f.Exists() {
			// If there was an error, treat it as a non-existant file
			// and just use the empty Sha1
			val = append(val, HashDiff{idx.PathName, idxtree, fs})
			continue
		}
		stat, err := f.Stat()
		if err != nil {
			val = append(val, HashDiff{idx.PathName, idxtree, fs})
			continue
		}

		switch {
		case stat.Mode().IsDir():
			fs.FileMode = ModeTree
		case !stat.Mode().IsRegular():
			// FIXME: This doesn't take into account that the file
			// might be some kind of non-symlink non-regular file.
			fs.FileMode = ModeSymlink
		case stat.Mode().Perm()&0100 != 0:
			fs.FileMode = ModeExec
		default:
			fs.FileMode = ModeBlob
		}
		fsHash, _, err := HashFile("blob", f.String())
		if err != nil {
			val = append(val, HashDiff{idx.PathName, idxtree, fs})
			continue
		}
		fs.Sha1 = fsHash
		if fs != idxtree {
			// the hash isn't in the git object store, so set it back to 0
			// after the comparison is done
			fs.Sha1 = Sha1{}
			val = append(val, HashDiff{idx.PathName, idxtree, fs})
		}
	}

	sort.Sort(ByName(val))

	return val, nil
}
