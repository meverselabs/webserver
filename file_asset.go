package webserver

import (
	"io"
	"net/http"
	"os"
)

type FileAsset struct {
	fs          http.FileSystem
	extraAssets []http.FileSystem
	path        string
}

func NewFileAsset(asset http.FileSystem, path string) *FileAsset {
	return &FileAsset{
		fs:          asset,
		extraAssets: []http.FileSystem{},
		path:        path,
	}
}

func (fa *FileAsset) checkDir(path string, f http.File, err error) (http.File, error) {
	if err != nil {
		return f, err
	}

	fi, err := f.Stat()
	if err != nil {
		return f, err
	}
	if !fi.IsDir() {
		return f, err
	}

	return &File{
		f,
		fa,
		path,
		map[string]struct{}{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	}, err
}

// A File is returned by a FileSystem's Open method and can be
// served by the FileServer implementation.
//
// The methods should behave the same as those on an *os.File.
type File struct {
	http.File
	fa     *FileAsset
	path   string
	readed map[string]struct{}

	localDisk      http.File
	localDiskErr   error
	localAssets    []http.File
	localAssetErrs []error
	assets         http.File
	assetErrs      error
}

func (f *File) Readdir(count int) ([]os.FileInfo, error) {
	mfi := map[string]int{}
	fis := []os.FileInfo{}
	{
		asset := http.Dir(f.fa.path)

		if f.localDisk == nil {
			f.localDisk, f.localDiskErr = asset.Open(f.path)
		}
		if f.localDiskErr == nil {
			files, err := f.localDisk.Readdir(count)
			if err == nil {
				for _, file := range files {
					f.readed[file.Name()] = struct{}{}
					if index, has := mfi[file.Name()]; has {
						fis[index] = file
					} else {
						mfi[file.Name()] = len(fis)
						fis = append(fis, file)
					}
				}
			} else if err != io.EOF {
				return nil, err
			}
		}
	}

	var err error
	for i, asset := range f.fa.extraAssets {
		if len(f.localAssets) <= i {
			of, err := asset.Open(f.path)
			f.localAssets = append(f.localAssets, of)
			f.localAssetErrs = append(f.localAssetErrs, err)
		}
		if f.localAssetErrs[i] == nil {
			fis, err = f.loadFiles(f.localAssets[i], fis, mfi, count)
		}
	}

	if len(fis) < count {
		if f.assets == nil {
			f.assets, f.assetErrs = f.fa.fs.Open(f.path)
		}
		if f.assetErrs == nil {
			fis, err = f.loadFiles(f.assets, fis, mfi, count)
		}
	}
	if err != nil && err != io.EOF {
		return nil, err
	}

	if len(fis) == 0 || count < 0 {
		return nil, io.EOF
	} else if len(fis) > count {
		return fis[:count], nil
	} else {
		return fis, nil
	}

}

func (f *File) loadFiles(assets http.File, fis []os.FileInfo, mfi map[string]int, count int) ([]os.FileInfo, error) {
	var fi []os.FileInfo
	var err error
	fi, err = assets.Readdir(1)
	for err == nil {
		file := fi[0]
		if _, has := f.readed[file.Name()]; has {
			fi, err = assets.Readdir(1)
			continue
		}
		f.readed[file.Name()] = struct{}{}
		if index, has := mfi[file.Name()]; has {
			fis[index] = file
		} else {
			mfi[file.Name()] = len(fis)
			fis = append(fis, file)
		}
		if len(fis) >= count {
			break
		}
		fi, err = assets.Readdir(1)
		if err != nil && err != io.EOF {
			return fis, err
		}
	}
	return fis, err
}

func (f *File) Stat() (os.FileInfo, error) {
	return f.File.Stat()
}

func (fa *FileAsset) Open(path string) (http.File, error) {
	asset := http.Dir(fa.path)
	f, err := asset.Open(path)
	if err == nil {
		return fa.checkDir(path, f, err)
	}

	for _, asset := range fa.extraAssets {
		f, err := asset.Open(path)
		if err == nil {
			return fa.checkDir(path, f, err)
		}
	}

	f, err = fa.fs.Open(path)
	return fa.checkDir(path, f, err)
}

func (fa *FileAsset) AddAssets(asset http.FileSystem) {
	fa.extraAssets = append(fa.extraAssets, asset)
}
