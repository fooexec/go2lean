package semantics

import "os"

// go2lean's own tests: the model's file system (osmodel). A file the
// program wrote reads back; one it did not write does not exist (an
// assumption the run records).

func fsDir() string {
	d, err := os.MkdirTemp("", "go2lean-fs-*")
	if err != nil {
		return ""
	}
	return d
}

func testFSWriteRead() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	if err := os.WriteFile(d+"/a.txt", []byte("hi"), 0o644); err != nil {
		return false
	}
	b, err := os.ReadFile(d + "/a.txt")
	return err == nil && string(b) == "hi"
}

func testFSOverwrite() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	os.WriteFile(d+"/a", []byte("one"), 0o644)
	os.WriteFile(d+"/a", []byte("two!"), 0o644)
	b, err := os.ReadFile(d + "/a")
	return err == nil && string(b) == "two!" && len(b) == 4
}

func testFSRemove() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	os.WriteFile(d+"/a", []byte("x"), 0o644)
	if err := os.Remove(d + "/a"); err != nil {
		return false
	}
	_, err := os.ReadFile(d + "/a")
	return err != nil && os.IsNotExist(err)
}

func testFSMkdirAllStat() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	if err := os.MkdirAll(d+"/x/y", 0o755); err != nil {
		return false
	}
	fi, err := os.Stat(d + "/x/y")
	if err != nil || !fi.IsDir() {
		return false
	}
	os.WriteFile(d+"/x/y/f", []byte("abc"), 0o644)
	fi, err = os.Stat(d + "/x/y/f")
	return err == nil && !fi.IsDir() && fi.Size() == 3 && fi.Name() == "f"
}

func testFSMkdirExists() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	if err := os.Mkdir(d+"/m", 0o755); err != nil {
		return false
	}
	err := os.Mkdir(d+"/m", 0o755)
	return err != nil && os.IsExist(err)
}

func testFSRemoveAll() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	os.MkdirAll(d+"/r/s", 0o755)
	os.WriteFile(d+"/r/s/f", []byte("x"), 0o644)
	if err := os.RemoveAll(d + "/r"); err != nil {
		return false
	}
	_, err := os.Stat(d + "/r/s/f")
	return os.IsNotExist(err) && os.RemoveAll(d+"/nothing") == nil
}

func testFSRemoveNonEmpty() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	os.MkdirAll(d+"/n", 0o755)
	os.WriteFile(d+"/n/f", []byte("x"), 0o644)
	return os.Remove(d+"/n") != nil
}

func testFSMissing() bool {
	d := fsDir()
	defer os.RemoveAll(d)
	_, err := os.ReadFile(d + "/never-written")
	return err != nil && os.IsNotExist(err)
}
