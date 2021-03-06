// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package ioutil implements some I/O utility functions.
package ioutil

import (
	"bytes"
	"io"
	"os"
	"sort"
	"sync"
)

// readAll reads from r until an error or EOF and returns the data it read
// from the internal buffer allocated with a specified capacity.
func readAll(r io.Reader, capacity int64) (b []byte, err error) {
	var buf bytes.Buffer

	// If the buffer overflows, we will get bytes.ErrTooLarge.
	// Return that as an error. Any other panic remains.
	defer func() {
		// 取恢复函数
		e := recover()
		if e == nil {
			return
		}

		// 抛异常
		if panicErr, ok := e.(error); ok && panicErr == bytes.ErrTooLarge {
			err = panicErr
		} else {
			panic(e)
		}
	}()

	// buffer 适配
	if int64(int(capacity)) == capacity {
		buf.Grow(int(capacity))
	}

	// 读取
	_, err = buf.ReadFrom(r)
	return buf.Bytes(), err
}

// ReadAll reads from r until an error or EOF and returns the data it read.
// A successful call returns err == nil, not err == EOF. Because ReadAll is
// defined to read from src until EOF, it does not treat an EOF from Read
// as an error to be reported.
func ReadAll(r io.Reader) ([]byte, error) {
	return readAll(r, bytes.MinRead)
}

// ReadFile reads the file named by filename and returns the contents.
// A successful call returns err == nil, not err == EOF. Because ReadFile
// reads the whole file, it does not treat an EOF from Read as an error
// to be reported.
func ReadFile(filename string) ([]byte, error) {
	// 打开文件
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	// 结束时关闭
	defer f.Close()

	// It's a good but not certain bet that FileInfo will tell us exactly how much to
	// read, so let's try it but be prepared for the answer to be wrong.
	var n int64 = bytes.MinRead

	if fi, err := f.Stat(); err == nil {
		// As initial capacity for readAll, use Size + a little extra in case Size
		// is zero, and to avoid another allocation after Read has filled the
		// buffer. The readAll call will read into its allocated internal buffer
		// cheaply. If the size was wrong, we'll either waste some space off the end
		// or reallocate as needed, but in the overwhelmingly common case we'll get
		// it just right.
		if size := fi.Size() + bytes.MinRead; size > n {
			n = size
		}
	}

	return readAll(f, n)
}

// WriteFile writes data to a file named by filename.
// If the file does not exist, WriteFile creates it with permissions perm;
// otherwise WriteFile truncates it before writing.
func WriteFile(filename string, data []byte, perm os.FileMode) error {
	// 打开文件
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}

	// 写数据
	n, err := f.Write(data)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}

	// 关闭
	if err1 := f.Close(); err == nil {
		err = err1
	}

	return err
}

// ReadDir reads the directory named by dirname and returns
// a list of directory entries sorted by filename.
func ReadDir(dirname string) ([]os.FileInfo, error) {
	// 打开目录
	f, err := os.Open(dirname)
	if err != nil {
		return nil, err
	}

	// 读取目录
	list, err := f.Readdir(-1)

	// 关闭目录
	f.Close()

	if err != nil {
		// 读取出错
		return nil, err
	}

	// 排序
	sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
	return list, nil
}

// 空Close 函数
type nopCloser struct {
	io.Reader
}
func (nopCloser) Close() error {
    return nil
}
// NopCloser returns a ReadCloser with a no-op Close method wrapping
// the provided Reader r.
// 创建函数
func NopCloser(r io.Reader) io.ReadCloser {
	return nopCloser{r}
}

// 空设备
type devNull int

// devNull implements ReaderFrom as an optimization so io.Copy to
// ioutil.Discard can avoid doing unnecessary work.
// 提示实现了ReadFrom
var _ io.ReaderFrom = devNull(0)

func (devNull) Write(p []byte) (int, error) {
    // 永远写成功
	return len(p), nil
}

func (devNull) WriteString(s string) (int, error) {
    // 永远写成功
	return len(s), nil
}

// 黑洞池
var blackHolePool = sync.Pool{
	New: func() interface{} {
	    // 返回8k 的buffer
		b := make([]byte, 8192)
		return &b
	},
}

// io.Copy，作为dst 时，可以调用此函数
func (devNull) ReadFrom(r io.Reader) (n int64, err error) {
    // 取buffer
	bufp := blackHolePool.Get().(*[]byte)

	readSize := 0
	for {
	    // 读取
		readSize, err = r.Read(*bufp)

        // 取大小
		n += int64(readSize)

		if err != nil {
		    // 资源回收
			blackHolePool.Put(bufp)

			if err == io.EOF {
			    // 成功
				return n, nil
			}

            // 失败
			return
		}
	}
}

// Discard is an io.Writer on which all Write calls succeed
// without doing anything.
// 假的
var Discard io.Writer = devNull(0)
