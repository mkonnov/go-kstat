//
// Package kstat provides a Go interface to the Solaris/OmniOS
// kstat(s) system for user-level access to a lot of kernel
// statistics. For more documentation on kstats, see kstat(1) and
// kstat(3kstat).
//
// In an ideal world the package documentation would go here. This is
// not an ideal world, because any number of tools like godoc choke on
// Go files that are not for their architecture (although I'll admit
// it's a hard problem).  So see doc.go for the actual package level
// documentation.
//
// However, I refuse to push function level API documentation off to another
// file, at least at the moment. It would be a horrible mess.
//

package kstat

// #cgo LDFLAGS: -lkstat
//
// #include <sys/types.h>
// #include <stdlib.h>
// #include <strings.h>
// #include <kstat.h>
//
// /* We have to reach through unions, which cgo doesn't support.
//    So we have our own cheesy little routines for it. These assume
//    they are always being called on validly-typed named kstats.
//  */
//
// char *get_named_char(kstat_named_t *knp) {
//	return knp->value.str.addr.ptr;
// }
//
// uint64_t get_named_uint(kstat_named_t *knp) {
//	if (knp->data_type == KSTAT_DATA_UINT32)
//		return knp->value.ui32;
//	else
//		return knp->value.ui64;
// }
//
// int64_t get_named_int(kstat_named_t *knp) {
//	if (knp->data_type == KSTAT_DATA_INT32)
//		return knp->value.i32;
//	else
//		return knp->value.i64;
// }
//
// /* Let's not try to do C pointer arithmetic in Go and get it wrong */
// kstat_named_t *get_nth_named(kstat_t *ks, uint_t n) {
//	kstat_named_t *knp;
//	if (!ks || !ks->ks_data || ks->ks_type != KSTAT_TYPE_NAMED || n >= ks->ks_ndata)
//		return NULL;
//	knp = KSTAT_NAMED_PTR(ks);
//	return knp + n;
// }
//
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Token is an access token for obtaining kstats.
type Token struct {
	kc *C.struct_kstat_ctl

	// ksm maps kstat_t pointers to our Go-level KStats for them.
	// kstat_t's stay constant over the lifetime of a token, so
	// we want to keep unique KStats. This holds some Go-level
	// memory down, but I wave my hands.
	ksm map[*C.struct_kstat]*KStat
}

// Open returns a kstat Token that is used to obtain kstats. It corresponds
// to kstat_open(). You should call .Close() when you're done and then not
// use any KStats or Nameds obtained through this token.
//
// (Failing to call .Close() will cause memory leaks.)
func Open() (*Token, error) {
	r, err := C.kstat_open()
	if r == nil {
		return nil, err
	}
	t := Token{}
	t.kc = r
	t.ksm = make(map[*C.struct_kstat]*KStat)
	return &t, nil
}

// Close a kstat access token. A closed token cannot be used for
// anything and cannot be reopened.
//
// After a Token has been closed it remains safe to look at fields
// on KStat and Named objects obtained through the Token, but it is
// not safe to call methods on them other than String(); doing so
// may cause memory corruption, although we try to avoid that.
//
// This corresponds to kstat_close().
func (t *Token) Close() error {
	if t.kc == nil {
		return nil
	}
	res, err := C.kstat_close(t.kc)
	t.kc = nil
	// clear the map to drop all references to KStats.
	t.ksm = make(map[*C.struct_kstat]*KStat)

	if res != 0 {
		return err
	}
	return nil
}

// All returns an array of all available KStats.
func (t *Token) All() []*KStat {
	n := []*KStat{}
	if t.kc == nil {
		return n
	}

	for r := t.kc.kc_chain; r != nil; r = r.ks_next {
		n = append(n, newKStat(t, r))
	}
	return n
}

//
// allocate a C string for a non-blank string
func maybeCString(src string) *C.char {
	if src == "" {
		return nil
	}
	return C.CString(src)
}

// free a non-nil C string
func maybeFree(cs *C.char) {
	if cs != nil {
		C.free(unsafe.Pointer(cs))
	}
}

// Lookup looks up a particular kstat. module and name may be "" and
// instance may be -1 to mean 'the first one that kstats can find'.
//
// Lookup() corresponds to kstat_lookup().
//
// Right now you cannot do anything useful with non-named kstats
// (as we don't provide any way to retrieve their data).
func (t *Token) Lookup(module string, instance int, name string) (*KStat, error) {
	if t == nil || t.kc == nil {
		return nil, errors.New("Token not valid or closed")
	}

	ms := maybeCString(module)
	ns := maybeCString(name)
	r, err := C.kstat_lookup(t.kc, ms, C.int(instance), ns)
	maybeFree(ms)
	maybeFree(ns)

	if r == nil {
		return nil, err
	}

	k := newKStat(t, r)
	// People rarely look up kstats to not use them, so we immediately
	// attempt to kstat_read() the data. If this fails, we don't return
	// the kstat. However, we don't scrub it from the kstat_t mapping
	// that the Token maintains; we have no reason to believe that it
	// needs to be remade. Our return of nil is a convenience to avoid
	// problems in callers.
	// TODO: this may be a mistake in the API.
	err = k.Refresh()
	if err != nil {
		return nil, err
	}
	return k, nil
}

// GetNamed obtains the Named representing a particular (named) kstat
// module:instance:name:statistic statistic.
//
// It is equivalent to .Lookup() then KStat.GetNamed().
func (t *Token) GetNamed(module string, instance int, name, stat string) (*Named, error) {
	stats, err := t.Lookup(module, instance, name)
	if err != nil {
		return nil, err
	}
	return stats.GetNamed(stat)
}

// -----

// KSType is the type of the data in a KStat.
type KSType int

// The different types of data that a KStat may contain, ie these
// are the value of a KStat.Type. We currently only support getting
// Named statistics.
const (
	RawStat   = C.KSTAT_TYPE_RAW
	NamedStat = C.KSTAT_TYPE_NAMED
	IntrStat  = C.KSTAT_TYPE_INTR
	IoStat    = C.KSTAT_TYPE_IO
	TimerStat = C.KSTAT_TYPE_TIMER
)

func (tp KSType) String() string {
	switch tp {
	case RawStat:
		return "raw"
	case NamedStat:
		return "named"
	case IntrStat:
		return "interrupt"
	case IoStat:
		return "io"
	case TimerStat:
		return "timer"
	default:
		return fmt.Sprintf("kstat_type:%d", tp)
	}
}

// KStat is the access handle for the collection of statistics for a
// particular module:instance:name kstat.
//
type KStat struct {
	Module   string
	Instance int
	Name     string

	// Class is eg 'net' or 'disk'. In kstat(1) it shows up as a
	// ':class' statistic.
	Class string
	// Type is the type of kstat. Named kstats are the only type
	// that can currently have their kstats data interpreted to
	// extract the stat values.
	Type KSType

	// Creation time of a kstat in nanoseconds since sometime.
	// See gethrtime(3) and kstat(3kstat).
	Crtime int64
	// Snaptime is what kstat(1) reports as 'snaptime', the time
	// that this data was obtained. As with Crtime, it is in
	// nanoseconds since some arbitrary point in time.
	// Snaptime may not be valid until .Refresh() or .GetNamed()
	// has been called.
	Snaptime int64

	ksp *C.struct_kstat
	// We need access to the token to refresh the data
	tok *Token
}

// newKStat is our internal KStat constructor.
//
// This also has the responsibility of maintaining (and using) the
// kstat_t to KStat mapping cache, so that we don't recreate new
// KStats for the same kstat_t all the time.
func newKStat(tok *Token, ks *C.struct_kstat) *KStat {
	if kst, ok := tok.ksm[ks]; ok {
		return kst
	}

	kst := KStat{}
	kst.ksp = ks
	kst.tok = tok

	kst.Instance = int(ks.ks_instance)
	kst.Module = C.GoString((*C.char)(unsafe.Pointer(&ks.ks_module)))
	kst.Name = C.GoString((*C.char)(unsafe.Pointer(&ks.ks_name)))
	kst.Class = C.GoString((*C.char)(unsafe.Pointer(&ks.ks_class)))
	kst.Type = KSType(ks.ks_type)
	kst.Crtime = int64(ks.ks_crtime)
	// TODO: we assume that ks_snaptime cannot be updated outside
	// of our control (in .Refresh). Is this true, or does Solaris
	// update it behind our backs?
	kst.Snaptime = int64(ks.ks_snaptime)

	tok.ksm[ks] = &kst
	return &kst
}

// invalid is a desperate attempt to keep usage errors from causing
// memory corruption. Don't count on it.
func (k *KStat) invalid() bool {
	return k == nil || k.ksp == nil || k.tok == nil || k.tok.kc == nil
}

// setup does validity checks and setup, such as loading data via Refresh().
func (k *KStat) setup() error {
	if k.invalid() {
		return errors.New("invalid KStat or closed token")
	}

	if k.ksp.ks_type != C.KSTAT_TYPE_NAMED {
		return fmt.Errorf("kstat %s (type %d) is not a named kstat", k, k.ksp.ks_type)
	}

	// Do the initial load of the data if necessary.
	if k.ksp.ks_data == nil {
		if err := k.Refresh(); err != nil {
			return err
		}
	}
	return nil
}

func (k *KStat) String() string {
	return fmt.Sprintf("%s:%d:%s (%s)", k.Module, k.Instance, k.Name, k.Class)
}

// Refresh the statistics data for a KStat.
//
// Note that this does not update any existing Named objects for
// statistics from this KStat. You must re-do .GetNamed() to get
// new ones in order to see any updates.
//
// Under the hood this does a kstat_read(). You don't need to call it
// explicitly before obtaining statistics from a KStat.
func (k *KStat) Refresh() error {
	if k.invalid() {
		return errors.New("invalid KStat or closed token")
	}

	res, err := C.kstat_read(k.tok.kc, k.ksp, nil)
	if res == -1 {
		return err
	}
	k.Snaptime = int64(k.ksp.ks_snaptime)
	return nil
}

// GetNamed obtains a particular named statistic from a kstat.
//
// It corresponds to kstat_data_lookup().
func (k *KStat) GetNamed(name string) (*Named, error) {
	if err := k.setup(); err != nil {
		return nil, err
	}
	ns := C.CString(name)
	r, err := C.kstat_data_lookup(k.ksp, ns)
	C.free(unsafe.Pointer(ns))
	if r == nil || err != nil {
		return nil, err
	}
	return newNamed(k, (*C.struct_kstat_named)(r)), err
}

// AllNamed returns an array of all named statistics for a particular
// named-type KStat. Entries are returned in no particular order.
func (k *KStat) AllNamed() ([]*Named, error) {
	if err := k.setup(); err != nil {
		return nil, err
	}
	lst := make([]*Named, k.ksp.ks_ndata)
	for i := C.uint_t(0); i < k.ksp.ks_ndata; i++ {
		ks := C.get_nth_named(k.ksp, i)
		if ks == nil {
			panic("get_nth_named returned surprise nil")
		}
		lst[i] = newNamed(k, ks)
	}
	return lst, nil
}

// Named represents a particular kstat named statistic, ie the full
//	module:instance:name:statistic
// and its current value.
//
// Name and Type are always valid, but only one of StringVal, IntVal,
// or UintVal is valid for any particular statistic; which one is
// valid is determined by its Type. Generally you'll already know what
// type a given named kstat statistic is; I don't believe Solaris
// changes their type once they're defined.
type Named struct {
	Name string
	Type NamedType

	// Only one of the following values is valid; the others are zero
	// values.
	//
	// StringVal holds the value for both CharData and String Type(s).
	StringVal string
	IntVal    int64
	UintVal   uint64

	// Pointer to the parent KStat, for access to the full name
	// and the snaptime/crtime associated with this Named.
	KStat *KStat
}

func (ks *Named) String() string {
	return fmt.Sprintf("%s:%d:%s:%s", ks.KStat.Module, ks.KStat.Instance, ks.KStat.Name, ks.Name)
}

// NamedType represents the various types of named kstat statistics.
type NamedType int

// The different types of data that a named kstat statistic can be
// (ie, these are the potential values of Named.Type).
const (
	CharData = C.KSTAT_DATA_CHAR
	Int32    = C.KSTAT_DATA_INT32
	Uint32   = C.KSTAT_DATA_UINT32
	Int64    = C.KSTAT_DATA_INT64
	Uint64   = C.KSTAT_DATA_UINT64
	String   = C.KSTAT_DATA_STRING

	// CharData is found in StringVal. At the moment we assume that
	// it is a real string, because this matches how it seems to be
	// used for short strings in the Solaris kernel. Someday we may
	// find something that uses it as just a data dump for 16 bytes.

	// Solaris sys/kstat.h also has _FLOAT (5) and _DOUBLE (6) types,
	// but labels them as obsolete.
)

func (tp NamedType) String() string {
	switch tp {
	case CharData:
		return "char"
	case Int32:
		return "int32"
	case Uint32:
		return "uint32"
	case Int64:
		return "int64"
	case Uint64:
		return "uint64"
	case String:
		return "string"
	default:
		return fmt.Sprintf("named_type-%d", tp)
	}
}

// Create a new Stat from the kstat_named_t
// We set the appropriate *Value field.
func newNamed(k *KStat, knp *C.struct_kstat_named) *Named {
	st := Named{}
	st.KStat = k
	st.Name = C.GoString((*C.char)(unsafe.Pointer(&knp.name)))
	st.Type = NamedType(knp.data_type)

	switch st.Type {
	case String:
		// The comments in sys/kstat.h explicitly guarantee
		// that these strings are null-terminated, although
		// knp.value.str.len also holds the length.
		st.StringVal = C.GoString(C.get_named_char(knp))
	case CharData:
		// Solaris/etc appears to use CharData for short strings
		// so that they can be embedded directly into
		// knp.value.c[16] instead of requiring an out of line
		// allocation. In theory we may find someone who is
		// using it as 128-bit ints or the like.
		// However I scanned the Illumos kernel source and
		// everyone using it appears to really be using it for
		// strings. We'll still bound the length.
		// (GoStringN does 'up to ...', fortunately.)
		st.StringVal = C.GoStringN((*C.char)(unsafe.Pointer(&knp.value)), 16)
	case Int32, Int64:
		st.IntVal = int64(C.get_named_int(knp))
	case Uint32, Uint64:
		st.UintVal = uint64(C.get_named_uint(knp))
	default:
		// TODO: should do better.
		panic(fmt.Sprintf("unknown stat type: %d", st.Type))
	}
	return &st
}
