// Copyright (c) 2015-2021 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bytes"
	"io"
	"os"
	"path"
	"testing"

	"github.com/qkbyte/minio/internal/lock"
)

func TestFSRenameFile(t *testing.T) {
	// create xlStorage test setup
	_, path, err := newXLStorageTestSetup(t)
	if err != nil {
		t.Fatalf("Unable to create xlStorage test setup, %s", err)
	}

	if err = fsMkdir(GlobalContext, pathJoin(path, "testvolume1")); err != nil {
		t.Fatal(err)
	}
	if err = fsRenameFile(GlobalContext, pathJoin(path, "testvolume1"), pathJoin(path, "testvolume2")); err != nil {
		t.Fatal(err)
	}
	if err = fsRenameFile(GlobalContext, pathJoin(path, "testvolume1"), pathJoin(path, "testvolume2")); err != errFileNotFound {
		t.Fatal(err)
	}
	if err = fsRenameFile(GlobalContext, pathJoin(path, "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001"), pathJoin(path, "testvolume2")); err != errFileNameTooLong {
		t.Fatal("Unexpected error", err)
	}
	if err = fsRenameFile(GlobalContext, pathJoin(path, "testvolume1"), pathJoin(path, "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001")); err != errFileNameTooLong {
		t.Fatal("Unexpected error", err)
	}
}

func TestFSStats(t *testing.T) {
	// create xlStorage test setup
	_, path, err := newXLStorageTestSetup(t)
	if err != nil {
		t.Fatalf("Unable to create xlStorage test setup, %s", err)
	}

	// Setup test environment.

	if err = fsMkdir(GlobalContext, ""); err != errInvalidArgument {
		t.Fatal("Unexpected error", err)
	}

	if err = fsMkdir(GlobalContext, pathJoin(path, "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001")); err != errFileNameTooLong {
		t.Fatal("Unexpected error", err)
	}

	if err = fsMkdir(GlobalContext, pathJoin(path, "success-vol")); err != nil {
		t.Fatalf("Unable to create volume, %s", err)
	}

	reader := bytes.NewReader([]byte("Hello, world"))
	if _, err = fsCreateFile(GlobalContext, pathJoin(path, "success-vol", "success-file"), reader, 0); err != nil {
		t.Fatalf("Unable to create file, %s", err)
	}
	// Seek back.
	reader.Seek(0, 0)

	if err = fsMkdir(GlobalContext, pathJoin(path, "success-vol", "success-file")); err != errVolumeExists {
		t.Fatal("Unexpected error", err)
	}

	if _, err = fsCreateFile(GlobalContext, pathJoin(path, "success-vol", "path/to/success-file"), reader, 0); err != nil {
		t.Fatalf("Unable to create file, %s", err)
	}
	// Seek back.
	reader.Seek(0, 0)

	testCases := []struct {
		srcFSPath   string
		srcVol      string
		srcPath     string
		expectedErr error
	}{
		// Test case - 1.
		// Test case with valid inputs, expected to pass.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "success-file",
			expectedErr: nil,
		},
		// Test case - 2.
		// Test case with valid inputs, expected to pass.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "path/to/success-file",
			expectedErr: nil,
		},
		// Test case - 3.
		// Test case with non-existent file.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "nonexistent-file",
			expectedErr: errFileNotFound,
		},
		// Test case - 4.
		// Test case with non-existent file path.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "path/2/success-file",
			expectedErr: errFileNotFound,
		},
		// Test case - 5.
		// Test case with path being a directory.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "path",
			expectedErr: errFileNotFound,
		},
		// Test case - 6.
		// Test case with src path segment > 255.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			expectedErr: errFileNameTooLong,
		},
		// Test case - 7.
		// Test case validate only srcVol exists.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			expectedErr: nil,
		},
		// Test case - 8.
		// Test case validate only srcVol doesn't exist.
		{
			srcFSPath:   path,
			srcVol:      "success-vol-non-existent",
			expectedErr: errVolumeNotFound,
		},
		// Test case - 9.
		// Test case validate invalid argument.
		{
			expectedErr: errInvalidArgument,
		},
	}

	for i, testCase := range testCases {
		if testCase.srcPath != "" {
			if _, err := fsStatFile(GlobalContext, pathJoin(testCase.srcFSPath, testCase.srcVol,
				testCase.srcPath)); err != testCase.expectedErr {
				t.Fatalf("TestErasureStorage case %d: Expected: \"%s\", got: \"%s\"", i+1, testCase.expectedErr, err)
			}
		} else {
			if _, err := fsStatVolume(GlobalContext, pathJoin(testCase.srcFSPath, testCase.srcVol)); err != testCase.expectedErr {
				t.Fatalf("TestFS case %d: Expected: \"%s\", got: \"%s\"", i+1, testCase.expectedErr, err)
			}
		}
	}
}

func TestFSCreateAndOpen(t *testing.T) {
	// Setup test environment.
	_, path, err := newXLStorageTestSetup(t)
	if err != nil {
		t.Fatalf("Unable to create xlStorage test setup, %s", err)
	}

	if err = fsMkdir(GlobalContext, pathJoin(path, "success-vol")); err != nil {
		t.Fatalf("Unable to create directory, %s", err)
	}

	if _, err = fsCreateFile(GlobalContext, "", nil, 0); err != errInvalidArgument {
		t.Fatal("Unexpected error", err)
	}

	if _, _, err = fsOpenFile(GlobalContext, "", -1); err != errInvalidArgument {
		t.Fatal("Unexpected error", err)
	}

	reader := bytes.NewReader([]byte("Hello, world"))
	if _, err = fsCreateFile(GlobalContext, pathJoin(path, "success-vol", "success-file"), reader, 0); err != nil {
		t.Fatalf("Unable to create file, %s", err)
	}
	// Seek back.
	reader.Seek(0, 0)

	testCases := []struct {
		srcVol      string
		srcPath     string
		expectedErr error
	}{
		// Test case - 1.
		// Test case with segment of the volume name > 255.
		{
			srcVol:      "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			srcPath:     "success-file",
			expectedErr: errFileNameTooLong,
		},
		// Test case - 2.
		// Test case with src path segment > 255.
		{
			srcVol:      "success-vol",
			srcPath:     "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			expectedErr: errFileNameTooLong,
		},
	}

	for i, testCase := range testCases {
		_, err = fsCreateFile(GlobalContext, pathJoin(path, testCase.srcVol, testCase.srcPath), reader, 0)
		if err != testCase.expectedErr {
			t.Errorf("Test case %d: Expected: \"%s\", got: \"%s\"", i+1, testCase.expectedErr, err)
		}
		_, _, err = fsOpenFile(GlobalContext, pathJoin(path, testCase.srcVol, testCase.srcPath), 0)
		if err != testCase.expectedErr {
			t.Errorf("Test case %d: Expected: \"%s\", got: \"%s\"", i+1, testCase.expectedErr, err)
		}
	}

	// Attempt to open a directory.
	if _, _, err = fsOpenFile(GlobalContext, pathJoin(path), 0); err != errIsNotRegular {
		t.Fatal("Unexpected error", err)
	}
}

func TestFSDeletes(t *testing.T) {
	// create xlStorage test setup
	_, path, err := newXLStorageTestSetup(t)
	if err != nil {
		t.Fatalf("Unable to create xlStorage test setup, %s", err)
	}

	// Setup test environment.
	if err = fsMkdir(GlobalContext, pathJoin(path, "success-vol")); err != nil {
		t.Fatalf("Unable to create directory, %s", err)
	}

	reader := bytes.NewReader([]byte("Hello, world"))
	if _, err = fsCreateFile(GlobalContext, pathJoin(path, "success-vol", "success-file"), reader, reader.Size()); err != nil {
		t.Fatalf("Unable to create file, %s", err)
	}
	// Seek back.
	reader.Seek(0, io.SeekStart)

	// folder is not empty
	err = fsMkdir(GlobalContext, pathJoin(path, "success-vol", "not-empty"))
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(pathJoin(path, "success-vol", "not-empty", "file"), []byte("data"), 0o777)
	if err != nil {
		t.Fatal(err)
	}

	// recursive
	if err = fsMkdir(GlobalContext, pathJoin(path, "success-vol", "parent")); err != nil {
		t.Fatal(err)
	}
	if err = fsMkdir(GlobalContext, pathJoin(path, "success-vol", "parent", "dir")); err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		basePath    string
		srcVol      string
		srcPath     string
		expectedErr error
	}{
		// valid case with existing volume and file to delete.
		{
			basePath:    path,
			srcVol:      "success-vol",
			srcPath:     "success-file",
			expectedErr: nil,
		},
		// The file was deleted in the last case, so Delete should fail.
		{
			basePath:    path,
			srcVol:      "success-vol",
			srcPath:     "success-file",
			expectedErr: errFileNotFound,
		},
		// Test case with segment of the volume name > 255.
		{
			basePath:    path,
			srcVol:      "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			srcPath:     "success-file",
			expectedErr: errFileNameTooLong,
		},
		// Test case with src path segment > 255.
		{
			basePath:    path,
			srcVol:      "success-vol",
			srcPath:     "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			expectedErr: errFileNameTooLong,
		},
		// Base path is way too long.
		{
			basePath:    "path03333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333333",
			srcVol:      "success-vol",
			srcPath:     "object",
			expectedErr: errFileNameTooLong,
		},
		// Directory is not empty. Should give nil, but won't delete.
		{
			basePath:    path,
			srcVol:      "success-vol",
			srcPath:     "not-empty",
			expectedErr: nil,
		},
		// Should delete recursively.
		{
			basePath:    path,
			srcVol:      "success-vol",
			srcPath:     pathJoin("parent", "dir"),
			expectedErr: nil,
		},
	}

	for i, testCase := range testCases {
		if err = fsDeleteFile(GlobalContext, testCase.basePath, pathJoin(testCase.basePath, testCase.srcVol, testCase.srcPath)); err != testCase.expectedErr {
			t.Errorf("Test case %d: Expected: \"%s\", got: \"%s\"", i+1, testCase.expectedErr, err)
		}
	}
}

func BenchmarkFSDeleteFile(b *testing.B) {
	// create xlStorage test setup
	_, path, err := newXLStorageTestSetup(b)
	if err != nil {
		b.Fatalf("Unable to create xlStorage test setup, %s", err)
	}

	// Setup test environment.
	if err = fsMkdir(GlobalContext, pathJoin(path, "benchmark")); err != nil {
		b.Fatalf("Unable to create directory, %s", err)
	}

	benchDir := pathJoin(path, "benchmark")
	filename := pathJoin(benchDir, "file.txt")

	b.ResetTimer()
	// We need to create and delete the file sequentially inside the benchmark.
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		err = os.WriteFile(filename, []byte("data"), 0o777)
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()

		err = fsDeleteFile(GlobalContext, benchDir, filename)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Tests fs removes.
func TestFSRemoves(t *testing.T) {
	// create xlStorage test setup
	_, path, err := newXLStorageTestSetup(t)
	if err != nil {
		t.Fatalf("Unable to create xlStorage test setup, %s", err)
	}

	// Setup test environment.
	if err = fsMkdir(GlobalContext, pathJoin(path, "success-vol")); err != nil {
		t.Fatalf("Unable to create directory, %s", err)
	}

	reader := bytes.NewReader([]byte("Hello, world"))
	if _, err = fsCreateFile(GlobalContext, pathJoin(path, "success-vol", "success-file"), reader, 0); err != nil {
		t.Fatalf("Unable to create file, %s", err)
	}
	// Seek back.
	reader.Seek(0, 0)

	if _, err = fsCreateFile(GlobalContext, pathJoin(path, "success-vol", "success-file-new"), reader, 0); err != nil {
		t.Fatalf("Unable to create file, %s", err)
	}
	// Seek back.
	reader.Seek(0, 0)

	testCases := []struct {
		srcFSPath   string
		srcVol      string
		srcPath     string
		expectedErr error
	}{
		// Test case - 1.
		// valid case with existing volume and file to delete.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "success-file",
			expectedErr: nil,
		},
		// Test case - 2.
		// The file was deleted in the last case, so Delete should fail.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "success-file",
			expectedErr: errFileNotFound,
		},
		// Test case - 3.
		// Test case with segment of the volume name > 255.
		{
			srcFSPath:   path,
			srcVol:      "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			srcPath:     "success-file",
			expectedErr: errFileNameTooLong,
		},
		// Test case - 4.
		// Test case with src path segment > 255.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			srcPath:     "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			expectedErr: errFileNameTooLong,
		},
		// Test case - 5.
		// Test case with src path empty.
		{
			srcFSPath:   path,
			srcVol:      "success-vol",
			expectedErr: errVolumeNotEmpty,
		},
		// Test case - 6.
		// Test case with src path empty.
		{
			srcFSPath:   path,
			srcVol:      "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001",
			expectedErr: errFileNameTooLong,
		},
		// Test case - 7.
		// Test case with src path empty.
		{
			srcFSPath:   path,
			srcVol:      "non-existent",
			expectedErr: errVolumeNotFound,
		},
		// Test case - 8.
		// Test case with src and volume path empty.
		{
			expectedErr: errInvalidArgument,
		},
	}

	for i, testCase := range testCases {
		if testCase.srcPath != "" {
			if err = fsRemoveFile(GlobalContext, pathJoin(testCase.srcFSPath, testCase.srcVol, testCase.srcPath)); err != testCase.expectedErr {
				t.Errorf("Test case %d: Expected: \"%s\", got: \"%s\"", i+1, testCase.expectedErr, err)
			}
		} else {
			if err = fsRemoveDir(GlobalContext, pathJoin(testCase.srcFSPath, testCase.srcVol, testCase.srcPath)); err != testCase.expectedErr {
				t.Error(err)
			}
		}
	}

	if err = fsRemoveAll(GlobalContext, pathJoin(path, "success-vol")); err != nil {
		t.Fatal(err)
	}

	if err = fsRemoveAll(GlobalContext, ""); err != errInvalidArgument {
		t.Fatal(err)
	}

	if err = fsRemoveAll(GlobalContext, "my-obj-del-0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001"); err != errFileNameTooLong {
		t.Fatal(err)
	}
}

func TestFSRemoveMeta(t *testing.T) {
	// create xlStorage test setup
	_, fsPath, err := newXLStorageTestSetup(t)
	if err != nil {
		t.Fatalf("Unable to create xlStorage test setup, %s", err)
	}

	// Setup test environment.
	if err = fsMkdir(GlobalContext, pathJoin(fsPath, "success-vol")); err != nil {
		t.Fatalf("Unable to create directory, %s", err)
	}

	filePath := pathJoin(fsPath, "success-vol", "success-file")

	reader := bytes.NewReader([]byte("Hello, world"))
	if _, err = fsCreateFile(GlobalContext, filePath, reader, 0); err != nil {
		t.Fatalf("Unable to create file, %s", err)
	}

	rwPool := &fsIOPool{
		readersMap: make(map[string]*lock.RLockedFile),
	}

	if _, err := rwPool.Open(filePath); err != nil {
		t.Fatalf("Unable to lock file %s", filePath)
	}

	defer rwPool.Close(filePath)

	tmpDir := t.TempDir()

	if err := fsRemoveMeta(GlobalContext, fsPath, filePath, tmpDir); err != nil {
		t.Fatalf("Unable to remove file, %s", err)
	}

	if _, err := os.Stat((filePath)); !osIsNotExist(err) {
		t.Fatalf("`%s` file found though it should have been deleted.", filePath)
	}

	if _, err := os.Stat((path.Dir(filePath))); !osIsNotExist(err) {
		t.Fatalf("`%s` parent directory found though it should have been deleted.", filePath)
	}
}

func TestFSIsFile(t *testing.T) {
	filePath := pathJoin(t.TempDir(), "tmpfile")

	if err := os.WriteFile(filePath, nil, 0o777); err != nil {
		t.Fatalf("Unable to create file %s", filePath)
	}

	if !fsIsFile(GlobalContext, filePath) {
		t.Fatalf("Expected %s to be a file", filePath)
	}
}
