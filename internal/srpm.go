package internal

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/cavaliercoder/go-cpio"
	"github.com/cavaliercoder/go-rpm"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type SrpmMode struct{}

func (s *SrpmMode) RetrieveSource(pd *ProcessData) *modeData {
	cmd := exec.Command("rpm2cpio", pd.RpmLocation)
	cpioBytes, err := cmd.Output()
	if err != nil {
		log.Fatalf("could not convert to cpio (maybe rpm2cpio is missing): %v", err)
	}

	// create in memory git repository
	repo, err := git.Init(memory.NewStorage(), memfs.New())
	if err != nil {
		log.Fatalf("could not init git repo: %v", err)
	}

	// read the rpm in cpio format
	buf := bytes.NewReader(cpioBytes)
	r := cpio.NewReader(buf)
	fileWrites := map[string][]byte{}
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			// end of cpio archive
			break
		}
		if err != nil {
			log.Fatalln(err)
		}

		bts, err := ioutil.ReadAll(r)
		if err != nil {
			log.Fatalf("could not copy file to virtual filesystem: %v", err)
		}
		fileWrites[hdr.Name] = bts
	}

	w, err := repo.Worktree()
	if err != nil {
		log.Fatalf("could not get worktree: %v", err)
	}

	// create structure
	err = w.Filesystem.MkdirAll("SPECS", 0755)
	if err != nil {
		log.Fatalf("could not create SPECS dir in vfs: %v", err)
	}
	err = w.Filesystem.MkdirAll("SOURCES", 0755)
	if err != nil {
		log.Fatalf("could not create SOURCES dir in vfs: %v", err)
	}

	f, err := os.Open(pd.RpmLocation)
	if err != nil {
		log.Fatalf("could not open the file again: %v", err)
	}
	rpmFile, err := rpm.ReadPackageFile(f)
	if err != nil {
		log.Fatalf("could not read package, invalid?: %v", err)
	}

	var sourcesToIgnore []*ignoredSource
	for _, source := range rpmFile.Source() {
		if strings.Contains(source, ".tar") {
			sourcesToIgnore = append(sourcesToIgnore, &ignoredSource{
				name:         source,
				hashFunction: sha256.New(),
			})
		}
	}

	branch := fmt.Sprintf("rocky%d", pd.Version)
	return &modeData{
		repo:            repo,
		worktree:        w,
		rpmFile:         rpmFile,
		fileWrites:      fileWrites,
		branches:        []string{branch},
		sourcesToIgnore: sourcesToIgnore,
	}
}

func (s *SrpmMode) WriteSource(md *modeData) {
	for fileName, contents := range md.fileWrites {
		var newPath string
		if filepath.Ext(fileName) == ".spec" {
			newPath = filepath.Join("SPECS", fileName)
		} else {
			newPath = filepath.Join("SOURCES", fileName)
		}

		mode := os.FileMode(0666)
		for _, file := range md.rpmFile.Files() {
			if file.Name() == fileName {
				mode = file.Mode()
			}
		}

		// add the file to the virtual filesystem
		// we will move it to correct destination later
		f, err := md.worktree.Filesystem.OpenFile(newPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			log.Fatalf("could not create file %s: %v", fileName, err)
		}

		_, err = f.Write(contents)
		if err != nil {
			log.Fatalf("could not write to file %s: %v", fileName, err)
		}

		_ = f.Close()

		// don't add ignored file to git
		if ignoredContains(md.sourcesToIgnore, fileName) {
			continue
		}

		_, err = md.worktree.Add(newPath)
		if err != nil {
			log.Fatalf("could not add source file: %v", err)
		}
	}

	// add sources to ignore (remote sources)
	gitIgnore, err := md.worktree.Filesystem.Create(".gitignore")
	if err != nil {
		log.Fatalf("could not create .gitignore: %v", err)
	}
	for _, ignore := range md.sourcesToIgnore {
		line := fmt.Sprintf("SOURCES/%s\n", ignore)
		_, err := gitIgnore.Write([]byte(line))
		if err != nil {
			log.Fatalf("could not write line to .gitignore: %v", err)
		}
	}
	err = gitIgnore.Close()
	if err != nil {
		log.Fatalf("could not close .gitignore: %v", err)
	}
}

func (s *SrpmMode) PostProcess(_ *modeData) {}

func (s *SrpmMode) ImportName(pd *ProcessData, _ *modeData) string {
	return filepath.Base(pd.RpmLocation)
}
