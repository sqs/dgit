package main

import (
	"fmt"
	libgit "github.com/driusan/git"
	"io/ioutil"
	"os"
	"strings"
)

func Fetch(repo *libgit.Repository, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Missing repository to fetch")
		return
	}

	file, err := os.Open(repo.Path + "/config")
	if err != nil {
		panic("Couldn't open config\n")
	}
	defer file.Close()
	config := parseConfig(repo, file)
	repoid := config.GetConfig("remote." + args[0] + ".url")
	var ups uploadpack
	if repoid[0:7] == "http://" || repoid[0:8] == "https://" {
		ups = smartHTTPServerRetriever{location: repoid,
			repo: repo,
		}
	} else {
		fmt.Fprintln(os.Stderr, "Unknown protocol.")
		return
	}
	refs, pack, err := ups.NegotiatePack()
	if err != nil {
		panic(err)
	}
	defer pack.Close()
	defer os.RemoveAll(pack.Name())
	pack.Seek(0, 0)
	fmt.Printf("Unpacking into %s\n", repo.Path)
	unpack(repo, pack)
	for _, ref := range refs {
		if repo.Path != "" {
			refloc := fmt.Sprintf("%s/%s", repo.Path, strings.TrimSpace(ref.Refname))
			refloc = strings.TrimSpace(refloc)
			fmt.Printf("Creating %s with %s", refloc, ref.Sha1)
			ioutil.WriteFile(
				refloc,
				[]byte(ref.Sha1),
				0644,
			)
		}
	}
}
