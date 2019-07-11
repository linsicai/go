// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package expvar provides a standardized interface to public variables, such
// as operation counters in servers. It exposes these variables via HTTP at
// /debug/vars in JSON format.
//
// Operations to set or modify these public variables are atomic.
//
// In addition to adding the HTTP handler, this package registers the
// following variables:
//
//	cmdline   os.Args
//	memstats  runtime.Memstats
//
// The package is sometimes only imported for the side effect of
// registering its HTTP handler and the above variables. To use it
// this way, link this package into your program:
//	import _ "expvar"
//
package expvar

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Var is an abstract type for all exported variables.
// 值
type Var interface {
	// String returns a valid JSON value for the variable.
	// Types with String methods that do not return valid JSON
	// (such as time.Time) must not be used as a Var.
	String() string
}

// Int is a 64-bit integer variable that satisfies the Var interface.
// int 值
type Int struct {
	i int64
}
func (v *Int) Value() int64 {
	return atomic.LoadInt64(&v.i)
}
func (v *Int) String() string {
	return strconv.FormatInt(atomic.LoadInt64(&v.i), 10)
}
func (v *Int) Add(delta int64) {
	atomic.AddInt64(&v.i, delta)
}
func (v *Int) Set(value int64) {
	atomic.StoreInt64(&v.i, value)
}

// Float is a 64-bit float variable that satisfies the Var interface.
// 非机器相关的浮点值
type Float struct {
	f uint64
}
func (v *Float) Value() float64 {
	return math.Float64frombits(atomic.LoadUint64(&v.f))
}
func (v *Float) String() string {
	return strconv.FormatFloat(
		math.Float64frombits(atomic.LoadUint64(&v.f)), 'g', -1, 64)
}
// Add adds delta to v.
func (v *Float) Add(delta float64) {
	for {
		cur := atomic.LoadUint64(&v.f)
		curVal := math.Float64frombits(cur)
		nxtVal := curVal + delta
		nxt := math.Float64bits(nxtVal)
		if atomic.CompareAndSwapUint64(&v.f, cur, nxt) {
			return
		}
	}
}
// Set sets v to value.
func (v *Float) Set(value float64) {
	atomic.StoreUint64(&v.f, math.Float64bits(value))
}

// Map is a string-to-Var map variable that satisfies the Var interface.
// 字典变量
type Map struct {
	m      sync.Map // map[string]Var

    // keys
	keysMu sync.RWMutex
	keys   []string // sorted
}

// KeyValue represents a single entry in a Map.
// 键值
type KeyValue struct {
	Key   string
	Value Var
}

func (v *Map) String() string {
	var b strings.Builder

    // 开始
	fmt.Fprintf(&b, "{")

    // 遍历，写键值
	first := true
	v.Do(func(kv KeyValue) {
		if !first {
			fmt.Fprintf(&b, ", ")
		}

		fmt.Fprintf(&b, "%q: %v", kv.Key, kv.Value)
		first = false
	})

    // 结束
	fmt.Fprintf(&b, "}")
	return b.String()
}

// Init removes all keys from the map.
func (v *Map) Init() *Map {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()

    // 键列表置为空
	v.keys = v.keys[:0]
	// 遍历删除kv
	v.m.Range(func(k, _ interface{}) bool {
		v.m.Delete(k)
		return true
	})
	return v
}

// addKey updates the sorted list of keys in v.keys.
// 增加key
func (v *Map) addKey(key string) {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()

	// Using insertion sort to place key into the already-sorted v.keys.
	if i := sort.SearchStrings(v.keys, key); i >= len(v.keys) {
	    // 插在最后
		v.keys = append(v.keys, key)
	} else if v.keys[i] != key {
	    // 插在中间
		v.keys = append(v.keys, "")
		copy(v.keys[i+1:], v.keys[i:])
		v.keys[i] = key
	}
}

// 读变量
func (v *Map) Get(key string) Var {
	i, _ := v.m.Load(key)
	av, _ := i.(Var)
	return av
}

// 写kv
func (v *Map) Set(key string, av Var) {
	// Before we store the value, check to see whether the key is new. Try a Load
	// before LoadOrStore: LoadOrStore causes the key interface to escape even on
	// the Load path.
	if _, ok := v.m.Load(key); !ok {
	    // 没有kv，cas写
		if _, dup := v.m.LoadOrStore(key, av); !dup {
		    // 补key
			v.addKey(key)
			return
		}
	}

    // 有kv，覆盖写
	v.m.Store(key, av)
}

// Add adds delta to the *Int value stored under the given map key.
func (v *Map) Add(key string, delta int64) {
	i, ok := v.m.Load(key)
	if !ok {
		var dup bool
		i, dup = v.m.LoadOrStore(key, new(Int))
		if !dup {
			v.addKey(key)
		}
	}

	// Add to Int; ignore otherwise.
	if iv, ok := i.(*Int); ok {
		iv.Add(delta)
	}
}

// AddFloat adds delta to the *Float value stored under the given map key.
func (v *Map) AddFloat(key string, delta float64) {
	i, ok := v.m.Load(key)
	if !ok {
		var dup bool
		i, dup = v.m.LoadOrStore(key, new(Float))
		if !dup {
			v.addKey(key)
		}
	}

	// Add to Float; ignore otherwise.
	if iv, ok := i.(*Float); ok {
		iv.Add(delta)
	}
}

// Deletes the given key from the map.
func (v *Map) Delete(key string) {
	v.keysMu.Lock()
	defer v.keysMu.Unlock()

    // 找位置
	i := sort.SearchStrings(v.keys, key)

    // 确认是对的后，删除
	if i < len(v.keys) && key == v.keys[i] {
		v.keys = append(v.keys[:i], v.keys[i+1:]...)
		v.m.Delete(key)
	}
}

// Do calls f for each entry in the map.
// The map is locked during the iteration,
// but existing entries may be concurrently updated.
func (v *Map) Do(f func(KeyValue)) {
	v.keysMu.RLock()
	defer v.keysMu.RUnlock()

    // 遍历可以，读值，执行函数
	for _, k := range v.keys {
		i, _ := v.m.Load(k)
		f(KeyValue{k, i.(Var)})
	}
}

// String is a string variable, and satisfies the Var interface.
// 字符串变量
type String struct {
	s atomic.Value // string
}
func (v *String) Value() string {
	p, _ := v.s.Load().(string)
	return p
}
// String implements the Var interface. To get the unquoted string
// use Value.
func (v *String) String() string {
	s := v.Value()
	b, _ := json.Marshal(s) // 序列化
	return string(b)
}
func (v *String) Set(value string) {
	v.s.Store(value)
}

// Func implements Var by calling the function
// and formatting the returned value using JSON.
// 函数变量
type Func func() interface{}
func (f Func) Value() interface{} {
	return f()
}
func (f Func) String() string {
	v, _ := json.Marshal(f())
	return string(v)
}

// All published variables.
var (
    // 变量映射表
	vars      sync.Map // map[string]Var

    // 变量键列表
	varKeysMu sync.RWMutex
	varKeys   []string // sorted
)

// Publish declares a named exported variable. This should be called from a
// package's init function when it creates its Vars. If the name is already
// registered then this will log.Panic.
func Publish(name string, v Var) {
	if _, dup := vars.LoadOrStore(name, v); dup {
	    // 重复注册
		log.Panicln("Reuse of exported var name:", name)
	}

    // 加锁
	varKeysMu.Lock()
	defer varKeysMu.Unlock()

    // 插入key
	varKeys = append(varKeys, name)
	sort.Strings(varKeys)
}

// Get retrieves a named exported variable. It returns nil if the name has
// not been registered.
// 读取值，转成var
func Get(name string) Var {
	i, _ := vars.Load(name)
	v, _ := i.(Var)
	return v
}

// Convenience functions for creating new exported variables.
// 注册int 值
func NewInt(name string) *Int {
	v := new(Int)
	Publish(name, v)
	return v
}
// 注册浮点值
func NewFloat(name string) *Float {
	v := new(Float)
	Publish(name, v)
	return v
}
func NewMap(name string) *Map {
	v := new(Map).Init()
	Publish(name, v)
	return v
}
func NewString(name string) *String {
	v := new(String)
	Publish(name, v)
	return v
}

// Do calls f for each exported variable.
// The global variable map is locked during the iteration,
// but existing entries may be concurrently updated.
// 遍历变量，执行函数
func Do(f func(KeyValue)) {
    // 加锁
	varKeysMu.RLock()
	defer varKeysMu.RUnlock()

    // 遍历key，取变量，运行函数
	for _, k := range varKeys {
		val, _ := vars.Load(k)
		f(KeyValue{k, val.(Var)})
	}
}

func expvarHandler(w http.ResponseWriter, r *http.Request) {
    // 设置类型
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

    // 遍历写kv
	fmt.Fprintf(w, "{\n")
	first := true
	Do(func(kv KeyValue) {
		if !first {
			fmt.Fprintf(w, ",\n")
		}
		first = false
		fmt.Fprintf(w, "%q: %s", kv.Key, kv.Value)
	})
	fmt.Fprintf(w, "\n}\n")
}

// Handler returns the expvar HTTP Handler.
//
// This is only needed to install the handler in a non-standard location.
func Handler() http.Handler {
	return http.HandlerFunc(expvarHandler)
}

func cmdline() interface{} {
	return os.Args
}

func memstats() interface{} {
	stats := new(runtime.MemStats)
	runtime.ReadMemStats(stats)
	return *stats
}

func init() {
	http.HandleFunc("/debug/vars", expvarHandler)

    // 命令行参数
	Publish("cmdline", Func(cmdline))

    // 内存统计
	Publish("memstats", Func(memstats))
}
