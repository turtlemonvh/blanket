package lib

import (
	"fmt"
	"github.com/mitchellh/go-homedir"
	"io"
	"os"
	"path"
	"path/filepath"
)

type FileCopier struct {
	BasePath string
}

// Find all files matching a pattern recursively
func (c *FileCopier) GetMatchingFiles(src string) ([]string, error) {
	var filesToCopy []string
	var err error

	// Clean up source path
	src, err = homedir.Expand(src)
	if err != nil {
		return filesToCopy, err
	}
	if !filepath.IsAbs(src) {
		src = path.Join(c.BasePath, src)
	}

	// Get matches to glob pattern
	var matches []string
	matches, err = filepath.Glob(src)
	if err != nil {
		return filesToCopy, err
	}

	// Loop over any matches, descending into directories
	for _, fileMatch := range matches {
		fileInfo, err := os.Stat(fileMatch)
		if err != nil {
			return filesToCopy, err
		}

		if !fileInfo.IsDir() {
			filesToCopy = append(filesToCopy, fileMatch)
		} else {
			err = filepath.Walk(fileMatch, func(path string, f os.FileInfo, err error) error {
				if !f.IsDir() {
					filesToCopy = append(filesToCopy, path)
				}
				return nil
			})
			if err != nil {
				return filesToCopy, err
			}
		}

	}

	return filesToCopy, nil
}

// Move a single file into the destination directory
// Creates non-existing directories
func (c *FileCopier) CopyFile(fileToCopy string, dest string) error {
	s, err := os.Open(fileToCopy)
	if err != nil {
		return err
	}
	defer s.Close()

	// FIXME: Check that this works for moving directories and maintains structure
	destFilepath := path.Join(dest, path.Base(fileToCopy))

	// Create path it if it doesn't exist
	err = os.MkdirAll(filepath.Dir(destFilepath), os.ModePerm)
	if err != nil {
		return err
	}

	d, err := os.Create(destFilepath)
	defer d.Close()
	if err != nil {
		return err
	}
	if _, err := io.Copy(d, s); err != nil {
		return err
	}

	return nil
}

func (c *FileCopier) CopyFiles(files [][]string, resultDir string) error {
	var err error

	for icFile, cFile := range files {
		// Validata files format
		if len(cFile) < 1 {
			return fmt.Errorf("The array of file information for item %d in the list 'files_to_include' must have at least 1 component", icFile)
		}

		// Find matches
		var filesToCopy []string
		filesToCopy, err = c.GetMatchingFiles(cFile[0])
		if err != nil {
			return err
		}

		// Get relative destination path
		dest := resultDir
		if len(cFile) > 1 {
			dest = path.Join(resultDir, cFile[1])
		}

		// Actual copy function
		for _, fileToCopy := range filesToCopy {
			err = c.CopyFile(fileToCopy, dest)
			if err != nil {
				return err
			}
		}
	}

	return err
}
