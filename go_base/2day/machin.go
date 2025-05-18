package test

import "unsafe"

type m struct {
	g0      *g     // 用于调度和执行系统调用的特殊goroutine
	morebuf gobuf  // gobuf arg to morestack
	divmod  uint32 // div/mod denominator for arm - known to liblink

	// Fields not known to debuggers.
	procid        uint64       // for debuggers, but offset not hard-coded
	gsignal       *g           // signal-handling g
	goSigStack    gsignalStack // Go-allocated signal handling stack
	sigmask       sigset       // storage for saved signal mask
	tls           [6]uintptr   // thread-local storage (for x86 extern register)
	mstartfn      func()
	curg          *g       // 当前正在运行的goroutine
	caughtsig     guintptr // goroutine running during fatal signal
	p             puintptr // 关联的P (执行Go代码时必须持有P)
	nextp         puintptr
	oldp          puintptr // 在执行系统调用之前的P
	id            int64
	mallocing     int32
	throwing      int32
	preemptoff    string // if != "", keep curg running on this m
	locks         int32
	dying         int32
	profilehz     int32
	spinning      bool // m is out of work and is actively looking for work
	blocked       bool // m is blocked on a note
	newSigstack   bool // minit on C thread called sigaltstack
	printlock     int8
	incgo         bool   // m is executing a cgo call
	freeWait      uint32 // if == 0, safe to free g0 and delete m (atomic)
	fastrand      [2]uint32
	needextram    bool
	traceback     uint8
	ncgocall      uint64      // number of cgo calls in total
	ncgo          int32       // number of cgo calls currently in progress
	cgoCallersUse uint32      // if non-zero, cgoCallers in use temporarily
	cgoCallers    *cgoCallers // cgo traceback if crashing in cgo call
	park          note
	alllink       *m // on allm
	schedlink     muintptr
	lockedg       guintptr
	createstack   [32]uintptr // stack that created this thread.
	lockedExt     uint32      // tracking for external LockOSThread
	lockedInt     uint32      // tracking for internal lockOSThread
	nextwaitm     muintptr    // next m waiting for lock
	waitunlockf   func(*g, unsafe.Pointer) bool
	waitlock      unsafe.Pointer
	waittraceev   byte
	waittraceskip int
	startingtrace bool
	syscalltick   uint32
	freelink      *m // on sched.freem

	// these are here because they are too large to be on the stack
	// of low-level NOSPLIT functions.
	libcall   libcall
	libcallpc uintptr // for cpu profiler
	libcallsp uintptr
	libcallg  guintptr
	syscall   libcall // stores syscall parameters on windows

	vdsoSP uintptr // SP for traceback while in VDSO call (0 if not in call)
	vdsoPC uintptr // PC for traceback while in VDSO call

	// preemptGen counts the number of completed preemption
	// signals. This is used to detect when a preemption is
	// requested, but fails. Accessed atomically.
	preemptGen uint32

	// Whether this is a pending preemption signal on this M.
	// Accessed atomically.
	signalPending uint32

	dlogPerM

	mOS
}

/* 什么时候会创建新的 m ？

1. P需要绑定M但无空闲M时
当有可运行的Goroutine但所有现有M都忙时：
触发场景：
P的本地队列有待运行G且无自旋M
GOMAXPROCS值调大后需要新M绑定新增的P


2. 执行CGO调用时
每个CGO调用会创建专用M（避免阻塞Go调度器）：
该M标记为needextram
调用结束后M会被回收


3. 系统调用阻塞时
当Goroutine执行阻塞式系统调用时：
典型场景：
文件IO、网络IO等阻塞调用
系统调用时间超过sysmon监控的阈值（默认20μs）

4. 监控线程(sysmon)触发
系统监控检测到M不足时：
触发条件：
持续存在超过10ms的可运行Goroutine
存在超过1ms的P空闲

5. 手动调用runtime.LockOSThread
当用户代码显式绑定Goroutine到线程时：
特殊用途：
线程本地存储(TLS)需求
需要固定线程的C库交互


6. 模板线程(Template Thread)创建
用于特殊场景的预备线程：
作用：
快速响应未来的newm请求
避免线程创建延迟影响关键路径
*/
