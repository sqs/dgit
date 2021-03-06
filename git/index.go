package git

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sort"
	"strings"
)

var InvalidIndex error = errors.New("Invalid index")

// index file is defined as Network byte Order (Big Endian)

// 12 byte header:
// 4 bytes: D I R C (stands for "Dir cache")
// 4-byte version number (can be 2, 3 or 4)
// 32bit number of index entries
type fixedGitIndex struct {
	Signature          [4]byte // 4
	Version            uint32  // 8
	NumberIndexEntries uint32  // 12
}

type Index struct {
	fixedGitIndex // 12
	Objects       []*IndexEntry
}
type IndexEntry struct {
	FixedIndexEntry

	PathName IndexPath
}

func (ie IndexEntry) Stage() Stage {
	return Stage((ie.Flags >> 12) & 0x3)
}
func NewIndex() *Index {
	return &Index{
		fixedGitIndex: fixedGitIndex{
			Signature:          [4]byte{'D', 'I', 'R', 'C'},
			Version:            2,
			NumberIndexEntries: 0,
		},
		Objects: make([]*IndexEntry, 0),
	}
}

type FixedIndexEntry struct {
	Ctime     uint32 // 16
	Ctimenano uint32 // 20

	Mtime     uint32 // 24
	Mtimenano uint32 // 28

	Dev uint32 // 32
	Ino uint32 // 36

	Mode EntryMode // 40

	Uid uint32 // 44
	Gid uint32 // 48

	Fsize uint32 // 52

	Sha1 Sha1 // 72

	Flags uint16 // 74
}

func (d GitDir) ReadIndex() (*Index, error) {
	file, err := d.Open("index")
	if err != nil {
		return &Index{
			fixedGitIndex{
				[4]byte{'D', 'I', 'R', 'C'},
				2, // version 2
				0, // no entries
			},
			make([]*IndexEntry, 0),
		}, err
	}
	defer file.Close()

	var i fixedGitIndex
	binary.Read(file, binary.BigEndian, &i)
	if i.Signature != [4]byte{'D', 'I', 'R', 'C'} {
		return nil, InvalidIndex
	}
	if i.Version < 2 || i.Version > 4 {
		return nil, InvalidIndex
	}

	var idx uint32
	indexes := make([]*IndexEntry, i.NumberIndexEntries, i.NumberIndexEntries)
	for idx = 0; idx < i.NumberIndexEntries; idx += 1 {
		if index, err := ReadIndexEntry(file); err == nil {
			indexes[idx] = index
		}
	}
	return &Index{i, indexes}, nil
}

func ReadIndexEntry(file *os.File) (*IndexEntry, error) {
	var f FixedIndexEntry
	var name []byte
	binary.Read(file, binary.BigEndian, &f)

	var nameLength uint16
	nameLength = f.Flags & 0x0FFF

	if nameLength&0xFFF != 0xFFF {
		name = make([]byte, nameLength, nameLength)
		n, err := file.Read(name)
		if err != nil {
			panic("I don't know what to do")
		}
		if n != int(nameLength) {
			panic("Error reading the name")
		}

		// I don't understand where this +4 comes from, but it seems to work
		// out with how the c git implementation calculates the padding..
		//
		// The definition of the index file format at:
		// https://github.com/git/git/blob/master/Documentation/technical/index-format.txt
		// claims that there should be "1-8 nul bytes as necessary to pad the entry to a multiple of eight
		// bytes while keeping the name NUL-terminated."
		//
		// The fixed size of the header is 82 bytes if you add up all the types.
		// the length of the name is nameLength bytes, so according to the spec
		// this *should* be 8 - ((82 + nameLength) % 8) bytes of padding.
		// But reading existant index files, there seems to be an extra 4 bytes
		// incorporated into the index size calculation.
		expectedOffset := 8 - ((82 + nameLength + 4) % 8)
		file.Seek(int64(expectedOffset), 1)
		/*
			This was used to verify that the offset is correct, but it causes problems if the data following
			the offset is empty..
			whitespace := make([]byte, 1, 1)
			var w uint16
			// Read all the whitespace that git uses for alignment.
			for _, _ = file.Read(whitespace); whitespace[0] == 0; _, _ = file.Read(whitespace) {
				w += 1
			}

			if w % 8 != expectedOffset {
				panic(fmt.Sprintf("Read incorrect number of whitespace characters %d vs %d", w, expectedOffset))
			}
			if w == 0 {
				panic("Name was not null terminated in index")
			}

			// Undo the last read, which wasn't whitespace..
			file.Seek(-1, 1)
		*/

	} else {
		panic("TODO: I can't handle such long names yet")
	}
	return &IndexEntry{f, IndexPath(name)}, nil
}

// A Stage represents a git merge stage in the index.
type Stage uint8

// Valid merge stages.
const (
	Stage0 = Stage(iota)
	Stage1
	Stage2
	Stage3
)

// Adds an entry to the index with Sha1 s and stage stage during a merge.
// If an entry already exists for this pathname/stage, it will be overwritten,
// otherwise it will be added.
//
// As a special case, if something is added as Stage0, then Stage1-3 entries
// will be removed.
func (g *Index) AddStage(c *Client, path IndexPath, s Sha1, stage Stage, mtime, mtimenano, size uint32) error {
	if stage == Stage0 {
		defer g.RemoveUnmergedStages(c, path)
	}

	// Update the existing stage, if it exists.
	for _, entry := range g.Objects {
		if entry.PathName == path && entry.Stage() == stage {
			entry.Sha1 = s
			entry.Mtime = mtime
			entry.Mtimenano = mtimenano
			entry.Fsize = size

			// We found and updated the entry, no need to continue
			return nil
		}
	}

	// There was no path/stage combo already in the index. Add it.

	// According to the git documentation:
	// Flags is
	//    A 16-bit 'flags' field split into (high to low bits)
	//
	//       1-bit assume-valid flag
	//
	//       1-bit extended flag (must be zero in version 2)
	//
	//       2-bit stage (during merge)
	//       12-bit name length if the length is less than 0xFFF; otherwise 0xFFF
	//     is stored in this field.

	// So we'll construct the flags based on what we know.

	var flags = uint16(stage) << 12 // start with the stage.
	// Add the name length.
	if len(path) >= 0x0FFF {
		flags |= 0x0FFF
	} else {
		flags |= (uint16(len(path)) & 0x0FFF)
	}

	g.Objects = append(g.Objects, &IndexEntry{
		FixedIndexEntry{
			0, //uint32(csec),
			0, //uint32(cnano),
			mtime,
			mtimenano,
			0,        //uint32(stat.Dev),
			0,        //uint32(stat.Ino),
			ModeBlob, // Directories are never added, only their files, so assume blob
			0,        //stat.Uid,
			0,        //stat.Gid,
			size,
			s,
			flags,
		},
		path,
	})
	g.NumberIndexEntries += 1
	sort.Sort(ByPath(g.Objects))
	return nil

}

// Remove any unmerged (non-stage 0) stage from the index for the given path
func (g *Index) RemoveUnmergedStages(c *Client, path IndexPath) error {
	// There are likely 3 things being deleted, so make a new slice
	newobjects := make([]*IndexEntry, 0, len(g.Objects))
	for _, entry := range g.Objects {
		stage := entry.Stage()
		if entry.PathName == path && stage == Stage0 {
			newobjects = append(newobjects, entry)
		} else if entry.PathName == path && stage != Stage0 {
			// do not add it, it's the wrong stage.
		} else {
			// It's a different Pathname, keep it.
			newobjects = append(newobjects, entry)
		}
	}
	g.Objects = newobjects
	g.NumberIndexEntries = uint32(len(newobjects))
	return nil
}

// Adds a file to the index, without writing it to disk.
// To write it to disk after calling this, use GitIndex.WriteIndex
//
// This will do the following:
// write git object blob of file contents to .git/objects
// normalize os.File name to path relative to gitRoot
// search GitIndex for normalized name
//	if GitIndexEntry found
//		update GitIndexEntry to point to the new object blob
// 	else
// 		add new GitIndexEntry if not found
//
func (g *Index) AddFile(c *Client, file *os.File) error {
	contents, err := ioutil.ReadAll(file)
	if err != nil {
		return err
	}
	hash, err := c.WriteObject("blob", contents)
	if err != nil && err != ObjectExists {
		fmt.Fprintf(os.Stderr, "Error storing object: %s", err)
		return err
	}
	name, err := File(file.Name()).IndexPath(c)
	if err != nil {
		return err
	}

	fstat, err := file.Stat()
	if err != nil {
		return err
	}
	if fstat.IsDir() {
		// This should really recursively call add for each file in the directory.
		return fmt.Errorf("Add can't handle directories. yet.")
	}

	modTime := fstat.ModTime()
	return g.AddStage(
		c,
		name,
		hash,
		Stage0,
		uint32(modTime.Unix()),
		uint32(modTime.Nanosecond()),
		uint32(fstat.Size()),
	)
}

type IndexStageEntry struct {
	IndexPath
	Stage
}

func (i *Index) GetStageMap() map[IndexStageEntry]*IndexEntry {
	r := make(map[IndexStageEntry]*IndexEntry)
	for _, entry := range i.Objects {
		r[IndexStageEntry{entry.PathName, entry.Stage()}] = entry
	}
	return r
}

type UnmergedPath struct {
	Stage1, Stage2, Stage3 *IndexEntry
}

func (i *Index) GetUnmerged() map[IndexPath]*UnmergedPath {
	r := make(map[IndexPath]*UnmergedPath)
	for _, entry := range i.Objects {
		if entry.Stage() != Stage0 {
			e, ok := r[entry.PathName]
			if !ok {
				e = &UnmergedPath{}
				r[entry.PathName] = e
			}
			switch entry.Stage() {
			case Stage1:
				e.Stage1 = entry
			case Stage2:
				e.Stage2 = entry
			case Stage3:
				e.Stage3 = entry
			}
		}
	}
	return r
}
func (i *Index) GetMap() map[IndexPath]*IndexEntry {
	r := make(map[IndexPath]*IndexEntry)
	for _, entry := range i.Objects {
		r[entry.PathName] = entry
	}
	return r
}

// Remove the first instance of file from the index. (This will usually
// be stage 0.)
func (g *Index) RemoveFile(file IndexPath) {
	for i, entry := range g.Objects {
		if entry.PathName == file {
			//println("Should remove ", i)
			g.Objects = append(g.Objects[:i], g.Objects[i+1:]...)
			g.NumberIndexEntries -= 1
			return
		}
	}
}

// This will write a new index file to w by doing the following:
// 1. Sort the objects in g.Index to ascending order based on name
// 2. Write g.fixedGitIndex to w
// 3. for each entry in g.Objects, write it to w.
// 4. Write the Sha1 of the contents of what was written
func (g Index) WriteIndex(file io.Writer) error {
	sort.Sort(ByPath(g.Objects))
	s := sha1.New()
	w := io.MultiWriter(file, s)
	binary.Write(w, binary.BigEndian, g.fixedGitIndex)
	for _, entry := range g.Objects {
		binary.Write(w, binary.BigEndian, entry.FixedIndexEntry)
		binary.Write(w, binary.BigEndian, []byte(entry.PathName))
		padding := 8 - ((82 + len(entry.PathName) + 4) % 8)
		p := make([]byte, padding)
		binary.Write(w, binary.BigEndian, p)
	}
	binary.Write(w, binary.BigEndian, s.Sum(nil))
	return nil
}

// Looks up the Sha1 of path currently stored in the index.
// Will return the 0 Sha1 if not found.
func (g Index) GetSha1(path IndexPath) Sha1 {
	for _, entry := range g.Objects {
		if entry.PathName == path {
			return entry.Sha1
		}
	}
	return Sha1{}
}

// Implement the sort interface on *GitIndexEntry, so that
// it's easy to sort by name.
type ByPath []*IndexEntry

func (g ByPath) Len() int      { return len(g) }
func (g ByPath) Swap(i, j int) { g[i], g[j] = g[j], g[i] }
func (g ByPath) Less(i, j int) bool {
	if g[i].PathName < g[j].PathName {
		return true
	} else if g[i].PathName == g[j].PathName {
		return g[i].Stage() < g[j].Stage()
	} else {
		return false
	}
}

func writeIndexSubtree(c *Client, prefix string, entries []*IndexEntry) (Sha1, error) {
	content := bytes.NewBuffer(nil)
	// [mode] [file/folder name]\0[SHA-1 of referencing blob or tree as [20]byte]

	lastname := ""
	firstIdxForTree := -1

	for idx, obj := range entries {
		relativename := strings.TrimPrefix(obj.PathName.String(), prefix+"/")
		//	fmt.Printf("This name: %s\n", relativename)
		nameBits := strings.Split(relativename, "/")

		// Either it's the last entry and we haven't written a tree yet, or it's not the last
		// entry but the directory changed
		if (nameBits[0] != lastname || idx == len(entries)-1) && lastname != "" {
			newPrefix := prefix + "/" + lastname

			var islice []*IndexEntry
			if idx == len(entries)-1 && nameBits[0] == lastname {
				islice = entries[firstIdxForTree:]
			} else {
				islice = entries[firstIdxForTree:idx]
			}
			subsha1, err := writeIndexSubtree(c, newPrefix, islice)
			if err != nil && err != ObjectExists {
				panic(err)
			}

			// Write the object
			fmt.Fprintf(content, "%o %s\x00", 0040000, lastname)
			content.Write(subsha1[:])

			if idx == len(entries)-1 && lastname != nameBits[0] {
				newPrefix := prefix + "/" + nameBits[0]
				subsha1, err := writeIndexSubtree(c, newPrefix, entries[len(entries)-1:])
				if err != nil && err != ObjectExists {
					panic(err)
				}

				// Write the object
				fmt.Fprintf(content, "%o %s\x00", 0040000, nameBits[0])
				content.Write(subsha1[:])

			}
			// Reset the data keeping track of what this tree is.
			lastname = ""
			firstIdxForTree = -1
		}
		if len(nameBits) == 1 {
			//write the blob for the file portion
			fmt.Fprintf(content, "%o %s\x00", obj.Mode, nameBits[0])
			content.Write(obj.Sha1[:])
			lastname = ""
			firstIdxForTree = -1
		} else {
			// calculate the sub-indexes to recurse on for this tree
			lastname = nameBits[0]
			if firstIdxForTree == -1 {
				firstIdxForTree = idx
			}
		}
	}

	return c.WriteObject("tree", content.Bytes())
}
func writeIndexEntries(c *Client, prefix string, entries []*IndexEntry) (TreeID, error) {
	content := bytes.NewBuffer(nil)
	// [mode] [file/folder name]\0[SHA-1 of referencing blob or tree as [20]byte]

	lastname := ""
	firstIdxForTree := -1

	for idx, obj := range entries {
		nameBits := strings.Split(obj.PathName.String(), "/")

		// Either it's the last entry and we haven't written a tree yet, or it's not the last
		// entry but the directory changed
		if (nameBits[0] != lastname || idx == len(entries)-1) && lastname != "" {
			var islice []*IndexEntry
			if idx == len(entries)-1 && nameBits[0] == lastname {
				islice = entries[firstIdxForTree:]
			} else {
				islice = entries[firstIdxForTree:idx]
			}
			subsha1, err := writeIndexSubtree(c, lastname, islice)
			if err != nil && err != ObjectExists {
				panic(err)
			}
			// Write the object
			fmt.Fprintf(content, "%o %s\x00", 0040000, lastname)
			content.Write(subsha1[:])

			// Reset the data keeping track of what this tree is.
			lastname = ""
			firstIdxForTree = -1
		}
		if len(nameBits) == 1 {
			//write the blob for the file portion
			fmt.Fprintf(content, "%o %s\x00", obj.Mode, obj.PathName)
			content.Write(obj.Sha1[:])
			lastname = ""
		} else {
			lastname = nameBits[0]
			if firstIdxForTree == -1 {
				firstIdxForTree = idx
			}
		}
	}

	tid, err := c.WriteObject("tree", content.Bytes())
	return TreeID(tid), err
}

// WriteTree writes the current index to a tree object.
// It returns the sha1 of the written tree, or an empty string
// if there was an error
func (g Index) WriteTree(c *Client) (TreeID, error) {
	sha1, err := writeIndexEntries(c, "", g.Objects)
	if err != nil && err != ObjectExists {
		return TreeID{}, err
	}
	return sha1, nil
}

// Replaces the index of Client with the the tree from the provided Treeish.
// if PreserveStatInfo is true, the stat information in the index won't be
// modified for existing entries.
func (g *Index) ResetIndex(c *Client, tree Treeish) error {
	newEntries, err := ExpandGitTreeIntoIndexes(c, tree, true, false)
	if err != nil {
		return err
	}
	g.NumberIndexEntries = uint32(len(newEntries))
	g.Objects = newEntries
	return nil
}
