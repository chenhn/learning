package test

// The main goroutine.
func main() {
	mp := getg().m

	// Racectx of m0->g0 is used only as the parent of the main goroutine.
	// It must not be used for anything else.
	mp.g0.racectx = 0

	// Max stack size is 1 GB on 64-bit, 250 MB on 32-bit.
	// Using decimal instead of binary GB and MB because
	// they look nicer in the stack overflow failure message.
	if goarch.PtrSize == 8 {
		maxstacksize = 1000000000
	} else {
		maxstacksize = 250000000
	}

	// An upper limit for max stack size. Used to avoid random crashes
	// after calling SetMaxStack and trying to allocate a stack that is too big,
	// since stackalloc works with 32-bit sizes.
	maxstackceiling = 2 * maxstacksize

	// Allow newproc to start new Ms.
	mainStarted = true // 标记主程序已启动：mainStarted = true 允许创建新的 M

	// 启动系统监控：如果系统支持 sysmon，则启动系统监控 goroutine
	if haveSysmon {
		systemstack(func() {
			newm(sysmon, nil, -1)
		})
	}

	// Lock the main goroutine onto this, the main OS thread,
	// during initialization. Most programs won't care, but a few
	// do require certain calls to be made by the main thread.
	// Those can arrange for main.main to run in the main thread
	// by calling runtime.LockOSThread during initialization
	// to preserve the lock.
	lockOSThread() // 绑定当前g0 到 m0 初始化代码在主线程运行

	if mp != &m0 {
		throw("runtime.main not on m0")
	}

	// Record when the world started.
	// Must be before doInit for tracing init.
	runtimeInitTime = nanotime()
	if runtimeInitTime == 0 {
		throw("nanotime returning zero")
	}

	if debug.inittrace != 0 {
		inittrace.id = getg().goid
		inittrace.active = true
	}

	doInit(runtime_inittasks) // Must be before defer.

	// Defer unlock so that runtime.Goexit during init does the unlock too.
	needUnlock := true
	defer func() {
		if needUnlock {
			unlockOSThread()
		}
	}()

	gcenable()

	main_init_done = make(chan bool)
	if iscgo {
		if _cgo_pthread_key_created == nil {
			throw("_cgo_pthread_key_created missing")
		}

		if _cgo_thread_start == nil {
			throw("_cgo_thread_start missing")
		}
		if GOOS != "windows" {
			if _cgo_setenv == nil {
				throw("_cgo_setenv missing")
			}
			if _cgo_unsetenv == nil {
				throw("_cgo_unsetenv missing")
			}
		}
		if _cgo_notify_runtime_init_done == nil {
			throw("_cgo_notify_runtime_init_done missing")
		}

		// Set the x_crosscall2_ptr C function pointer variable point to crosscall2.
		if set_crosscall2 == nil {
			throw("set_crosscall2 missing")
		}
		set_crosscall2()

		// Start the template thread in case we enter Go from
		// a C-created thread and need to create a new thread.
		startTemplateThread()
		cgocall(_cgo_notify_runtime_init_done, nil)
	}

	// Run the initializing tasks. Depending on build mode this
	// list can arrive a few different ways, but it will always
	// contain the init tasks computed by the linker for all the
	// packages in the program (excluding those added at runtime
	// by package plugin). Run through the modules in dependency
	// order (the order they are initialized by the dynamic
	// loader, i.e. they are added to the moduledata linked list).
	for m := &firstmoduledata; m != nil; m = m.next {
		doInit(m.inittasks)
	}

	// Disable init tracing after main init done to avoid overhead
	// of collecting statistics in malloc and newproc
	inittrace.active = false

	close(main_init_done)

	needUnlock = false
	unlockOSThread()

	if isarchive || islibrary {
		// A program compiled with -buildmode=c-archive or c-shared
		// has a main, but it is not executed.
		if GOARCH == "wasm" {
			// On Wasm, pause makes it return to the host.
			// Unlike cgo callbacks where Ms are created on demand,
			// on Wasm we have only one M. So we keep this M (and this
			// G) for callbacks.
			// Using the caller's SP unwinds this frame and backs to
			// goexit. The -16 is: 8 for goexit's (fake) return PC,
			// and pause's epilogue pops 8.
			pause(sys.GetCallerSP() - 16) // should not return
			panic("unreachable")
		}
		return
	}
	fn := main_main // make an indirect call, as the linker doesn't know the address of the main package when laying down the runtime
	fn()
	if raceenabled {
		runExitHooks(0) // run hooks now, since racefini does not return
		racefini()
	}

	// Make racy client program work: if panicking on
	// another goroutine at the same time as main returns,
	// let the other goroutine finish printing the panic trace.
	// Once it does, it will exit. See issues 3934 and 20018.
	if runningPanicDefers.Load() != 0 {
		// Running deferred functions should not take long.
		for c := 0; c < 1000; c++ {
			if runningPanicDefers.Load() == 0 {
				break
			}
			Gosched()
		}
	}
	if panicking.Load() != 0 {
		gopark(nil, nil, waitReasonPanicWait, traceBlockForever, 1)
	}
	runExitHooks(0)

	exit(0)
	for {
		var x *int32
		*x = 0
	}
}
