//go:build windows

package mstsc

// The small part of COM the control needs: interface pointers with vtable
// calls, BSTR, VARIANT and late-bound IDispatch calls by name. Every call
// must be made on the UI thread.

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	oleaut32              = syscall.NewLazyDLL("oleaut32.dll")
	procSysAllocStringLen = oleaut32.NewProc("SysAllocStringLen")
	procSysFreeString     = oleaut32.NewProc("SysFreeString")
	procSysStringLen      = oleaut32.NewProc("SysStringLen")
	procVariantClear      = oleaut32.NewProc("VariantClear")
	ole32                 = syscall.NewLazyDLL("ole32.dll")
	procOleInitialize     = ole32.NewProc("OleInitialize")
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procRtlZeroMemory     = kernel32.NewProc("RtlZeroMemory")
	procGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")
)

// hresult is a failed HRESULT.
type hresult uint32

func (hr hresult) Error() string { return fmt.Sprintf("HRESULT %#08x", uint32(hr)) }

// HRESULTs (winerror.h).
const (
	sOK              = 0x00000000
	eNotImpl         = 0x80004001
	eNoInterface     = 0x80004002
	ePointer         = 0x80004003
	dispEUnknownName = 0x80020006
	dispEException   = 0x80020009
)

// check turns the return value of a COM call into an error.
func check(r uintptr) error {
	if int32(r) < 0 {
		return hresult(uint32(r))
	}
	return nil
}

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

// mustGUID parses "8C11EFAE-92C3-11D1-BC1E-00C04FA31489".
func mustGUID(s string) guid {
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		panic("bad GUID " + s)
	}
	n := func(part string) uint64 {
		v, err := strconv.ParseUint(part, 16, 64)
		if err != nil {
			panic("bad GUID " + s)
		}
		return v
	}
	g := guid{d1: uint32(n(h[0:8])), d2: uint16(n(h[8:12])), d3: uint16(n(h[12:16]))}
	for i := range g.d4 {
		g.d4[i] = byte(n(h[16+2*i : 18+2*i]))
	}
	return g
}

// iface is a COM object as seen through one of its interfaces: its first
// word points to the vtable. The memory belongs to the object, not to Go.
type iface struct {
	vtbl *[128]uintptr
}

// fn is the function in a vtable slot, for syscall.SyscallN.
func (p *iface) fn(slot int) uintptr { return p.vtbl[slot] }

// call calls the method in a vtable slot with arguments that are plain
// numbers. Calls that pass pointers use SyscallN directly, so the pointers
// stay valid during the call.
func (p *iface) call(slot int, args ...uintptr) error {
	a := make([]uintptr, 0, 1+len(args))
	a = append(a, uintptr(unsafe.Pointer(p)))
	a = append(a, args...)
	r, _, _ := syscall.SyscallN(p.fn(slot), a...)
	return check(r)
}

// query is QueryInterface; nil when the object lacks the interface.
func (p *iface) query(iid *guid) *iface {
	var out *iface
	r, _, _ := syscall.SyscallN(p.fn(0), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if check(r) != nil {
		return nil
	}
	return out
}

// release is Release; it accepts nil.
func (p *iface) release() {
	if p != nil {
		syscall.SyscallN(p.fn(2), uintptr(unsafe.Pointer(p)))
	}
}

// bstr is a BSTR. The zero value is the null BSTR (an empty string).
type bstr uintptr

func newBSTR(s string) bstr {
	u, err := syscall.UTF16FromString(s)
	if err != nil || len(u) <= 1 {
		return 0
	}
	b, _, _ := procSysAllocStringLen.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1))
	clear(u) // it may be a password
	return bstr(b)
}

func (b bstr) free() {
	if b != 0 {
		procSysFreeString.Call(uintptr(b))
	}
}

// wipe overwrites the string with zeros (for a password) and frees it.
func (b bstr) wipe() {
	if b != 0 {
		n, _, _ := procSysStringLen.Call(uintptr(b))
		procRtlZeroMemory.Call(uintptr(b), n*2)
		b.free()
	}
}

// takeString returns the text of a BSTR received from COM and frees it.
func takeString(p *uint16) string {
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	procSysFreeString.Call(uintptr(unsafe.Pointer(p)))
	return s
}

// VARIANT types (wtypes.h).
const (
	vtEmpty    = 0
	vtI2       = 2
	vtI4       = 3
	vtBSTR     = 8
	vtDispatch = 9
	vtBool     = 11
	vtUI4      = 19
	vtInt      = 22
	vtUint     = 23
)

// variant is VARIANT: 24 bytes on 64 bit Windows, 16 on 32 bit.
type variant struct {
	vt         uint16
	r1, r2, r3 uint16
	val        uintptr
	_          uintptr
}

func varInt(v int) variant     { return variant{vt: vtI4, val: uintptr(uint32(int32(v)))} }
func varUint(v uint32) variant { return variant{vt: vtUI4, val: uintptr(v)} }
func varBSTR(b bstr) variant   { return variant{vt: vtBSTR, val: uintptr(b)} }

func varBool(v bool) variant {
	if v {
		return variant{vt: vtBool, val: 0xFFFF} // VARIANT_TRUE
	}
	return variant{vt: vtBool}
}

// variantBool is VARIANT_BOOL as a vtable call argument.
func variantBool(v bool) uintptr {
	if v {
		return 0xFFFF
	}
	return 0
}

// int returns a numeric or boolean variant as a number, 0 for other types.
func (v *variant) int() int {
	switch v.vt {
	case vtI4, vtUI4, vtInt, vtUint:
		return int(int32(uint32(v.val)))
	case vtI2, vtBool:
		return int(int16(uint16(v.val)))
	}
	return 0
}

// dispatch returns the object in a VT_DISPATCH variant, nil for other types.
// The reference the variant holds goes to the caller.
func (v *variant) dispatch() *iface {
	if v.vt != vtDispatch {
		return nil
	}
	return *(**iface)(unsafe.Pointer(&v.val))
}

// clear frees what the variant holds.
func (v *variant) clear() {
	procVariantClear.Call(uintptr(unsafe.Pointer(v)))
}

// dispParams is DISPPARAMS.
type dispParams struct {
	args      *variant // in reverse order
	namedArgs *int32
	nArgs     uint32
	nNamed    uint32
}

// excepInfo is EXCEPINFO.
type excepInfo struct {
	code, reserved                uint16
	source, description, helpFile *uint16
	helpContext                   uint32
	pvReserved, deferredFillIn    uintptr
	scode                         int32
}

const (
	dispatchMethod      = 1
	dispatchPropertyGet = 2
	dispatchPropertyPut = 4
	dispidPropertyPut   = -3
)

// dispatch is a scriptable object called by name. Names that the object
// does not have are remembered and logged once.
type dispatch struct {
	p    *iface
	what string // for the log: "client", "advanced settings" ...
	ids  map[string]int32
	miss map[string]bool
}

func newDispatch(p *iface, what string) *dispatch {
	if p == nil {
		return nil
	}
	return &dispatch{p: p, what: what, ids: map[string]int32{}, miss: map[string]bool{}}
}

func (d *dispatch) release() {
	if d != nil {
		d.p.release()
		d.p = nil
	}
}

// id looks up the DISPID of a property or method.
func (d *dispatch) id(name string) (int32, bool) {
	if d == nil || d.p == nil {
		return 0, false
	}
	if id, ok := d.ids[name]; ok {
		return id, true
	}
	if d.miss[name] {
		return 0, false
	}
	u, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, false
	}
	var null guid
	var id int32
	r, _, _ := syscall.SyscallN(d.p.fn(slotGetIDsOfNames), uintptr(unsafe.Pointer(d.p)),
		uintptr(unsafe.Pointer(&null)), uintptr(unsafe.Pointer(&u)), 1, 0, uintptr(unsafe.Pointer(&id)))
	if err := check(r); err != nil {
		d.miss[name] = true
		slog.Warn("rdp control: name not supported", "object", d.what, "name", name, "err", err)
		return 0, false
	}
	d.ids[name] = id
	return id, true
}

// invoke calls IDispatch::Invoke. Args are in natural order.
func (d *dispatch) invoke(id int32, flags uint16, args []variant) (variant, error) {
	// Invoke wants the arguments last to first.
	rev := make([]variant, len(args))
	for i, a := range args {
		rev[len(args)-1-i] = a
	}
	var dp dispParams
	if len(rev) > 0 {
		dp.args, dp.nArgs = &rev[0], uint32(len(rev))
	}
	named := int32(dispidPropertyPut)
	if flags == dispatchPropertyPut {
		dp.namedArgs, dp.nNamed = &named, 1
	}
	var null guid
	var result variant
	var ex excepInfo
	var argErr uint32
	r, _, _ := syscall.SyscallN(d.p.fn(slotInvoke), uintptr(unsafe.Pointer(d.p)),
		uintptr(uint32(id)), uintptr(unsafe.Pointer(&null)), 0, uintptr(flags),
		uintptr(unsafe.Pointer(&dp)), uintptr(unsafe.Pointer(&result)),
		uintptr(unsafe.Pointer(&ex)), uintptr(unsafe.Pointer(&argErr)))
	err := check(r)
	if uint32(r) == dispEException {
		// The method itself failed: its own HRESULT is in the exception.
		takeString(ex.source)
		takeString(ex.helpFile)
		text := takeString(ex.description)
		if ex.scode != 0 {
			err = hresult(uint32(ex.scode))
		}
		if text != "" {
			err = fmt.Errorf("%w: %s", err, text)
		}
	}
	return result, err
}

// put sets a property and logs a failure. False when it was not set.
func (d *dispatch) put(name string, v variant) bool {
	id, ok := d.id(name)
	if !ok {
		return false
	}
	if _, err := d.invoke(id, dispatchPropertyPut, []variant{v}); err != nil {
		slog.Warn("rdp control: set failed", "object", d.what, "property", name, "err", err)
		return false
	}
	return true
}

func (d *dispatch) putBool(name string, v bool) bool { return d.put(name, varBool(v)) }
func (d *dispatch) putInt(name string, v int) bool   { return d.put(name, varInt(v)) }

func (d *dispatch) putString(name, v string) bool {
	b := newBSTR(v)
	defer b.free()
	return d.put(name, varBSTR(b))
}

// get reads a property and logs a failure. The caller clears the result.
func (d *dispatch) get(name string) (variant, bool) {
	id, ok := d.id(name)
	if !ok {
		return variant{}, false
	}
	v, err := d.invoke(id, dispatchPropertyGet, nil)
	if err != nil {
		slog.Warn("rdp control: get failed", "object", d.what, "property", name, "err", err)
		return variant{}, false
	}
	return v, true
}

// getInt reads a numeric property.
func (d *dispatch) getInt(name string) (int, bool) {
	v, ok := d.get(name)
	if !ok {
		return 0, false
	}
	defer v.clear()
	return v.int(), true
}

// getObject reads a property that is an object.
func (d *dispatch) getObject(name, what string) *dispatch {
	v, ok := d.get(name)
	if !ok {
		return nil
	}
	p := v.dispatch()
	if p == nil {
		v.clear()
		return nil
	}
	return newDispatch(p, what)
}

// method calls a method. The error is not logged: the caller knows whether
// a failure is expected.
func (d *dispatch) method(name string, args ...variant) error {
	id, ok := d.id(name)
	if !ok {
		return hresult(dispEUnknownName)
	}
	v, err := d.invoke(id, dispatchMethod, args)
	v.clear()
	return err
}

// getString reads a text property, "" when it fails.
func (d *dispatch) getString(name string) string {
	v, ok := d.get(name)
	if !ok {
		return ""
	}
	defer v.clear()
	if v.vt != vtBSTR || v.val == 0 {
		return ""
	}
	return windows.UTF16PtrToString(*(**uint16)(unsafe.Pointer(&v.val)))
}
