//go:build scheduler.cores

package runtime

import (
	"internal/task"
	"runtime/interrupt"
	"sync/atomic"
)

const hasScheduler = true

const hasParallelism = true

var mainExited atomic.Uint32

// Which task is running on a given core (or nil if there is no task running on
// the core).
var cpuTasks [numCPU]*task.Task

var (
	sleepQueue *task.Task
	runQueue   *task.Task
)

func deadlock() {
	// Call yield without requesting a wakeup.
	task.Pause()
	trap()
}

// Mark the given task as ready to resume.
// This is allowed even if the task isn't paused yet, but will pause soon.
func scheduleTask(t *task.Task) {
	schedulerLock.Lock()
	switch t.RunState {
	case task.RunStatePaused:
		// Paused, state is saved on the stack.
		// Add it to the runqueue...
		addToRunQueue(t)
		// ...and wake up a sleeping core, if there is one.
		// (If all cores are already busy, this is a no-op).
		schedulerWake()
	case task.RunStateRunning:
		// Not yet paused (probably going to pause very soon), so let the
		// Pause() function know it can resume immediately.
		t.RunState = task.RunStateResuming
	default:
		if schedulerAsserts {
			runtimePanic("scheduler: unknown run state")
		}
	}
	schedulerLock.Unlock()
}

// Add task to runQueue.
// Scheduler lock must be held when calling this function.
func addToRunQueue(t *task.Task) {
	t.Next = runQueue
	runQueue = t
}

func addSleepTask(t *task.Task, wakeup timeUnit) {
	// Save the timestamp when the task should be woken up.
	t.Data = uint64(wakeup)

	// Find the position where we should insert this task in the queue.
	q := &sleepQueue
	for {
		if *q == nil {
			// Found the end of the time queue. Insert it here, at the end.
			break
		}
		if timeUnit((*q).Data) > timeUnit(t.Data) {
			// Found a task in the queue that has a timeout before the
			// to-be-sleeping task. Insert our task right before.
			break
		}
		q = &(*q).Next
	}

	// Insert the task into the queue (this could be at the end, if *q is nil).
	t.Next = *q
	*q = t
}

func Gosched() {
	addToRunQueue(task.Current())
	task.Pause()
}

func addTimer(tn *timerNode) {
	runtimePanic("todo: timers")
}

func removeTimer(t *timer) bool {
	runtimePanic("todo: timers")
	return false
}

func schedulerRunQueue() *task.Queue {
	// This should not be reachable with the cores scheduler.
	runtimePanic("unimplemented: schedulerRunQueue")
	return nil
}

// Pause the current task for a given time.
//
//go:linkname sleep time.Sleep
func sleep(duration int64) {
	if duration <= 0 {
		return
	}

	wakeup := ticks() + nanosecondsToTicks(duration)

	// While the scheduler is locked:
	// - add this task to the sleep queue
	// - switch to the scheduler (only allowed while locked)
	// - let the scheduler handle it from there
	schedulerLock.Lock()
	addSleepTask(task.Current(), wakeup)
	task.PauseLocked()
}

// This function is called on the first core in the system. It will wake up the
// other cores when ready.
func run() {
	initHeap()

	go func() {
		// Package initializers are currently run single-threaded.
		// This might help with registering interrupts and such.
		initAll()

		// After package initializers have finished, start all the other cores.
		startSecondaryCores()

		// Run main.main.
		callMain()

		// main.main has exited, so the program should exit.
		mainExited.Store(1)
	}()

	// The scheduler must always be entered while the scheduler lock is taken.
	schedulerLock.Lock()
	scheduler(false)
	schedulerLock.Unlock()
}

var schedulerIsRunning = false

func scheduler(_ bool) {
	for mainExited.Load() == 0 {
		// Check for ready-to-run tasks.
		if runnable := runQueue; runnable != nil {
			// Pop off the run queue.
			runQueue = runnable.Next
			runnable.Next = nil

			// Resume it now.
			setCurrentTask(runnable)
			runnable.RunState = task.RunStateRunning
			schedulerLock.Unlock() // unlock before resuming, Pause() will lock again
			runnable.Resume()
			setCurrentTask(nil)

			continue
		}

		// If another core is using the clock, let it handle the sleep queue.
		if schedulerIsRunning {
			schedulerUnlockAndWait()
			continue
		}

		// If there are any sleeping tasks, we should either run it now (if it's
		// done waiting) or sleep until it is runnable.
		if sleepingTask := sleepQueue; sleepingTask != nil {
			now := ticks()

			// Check whether the first task in the sleep queue is ready to run.
			if now >= timeUnit(sleepingTask.Data) {
				// It is, resume it now.
				sleepQueue = sleepQueue.Next
				sleepingTask.Next = nil

				setCurrentTask(sleepingTask)
				sleepingTask.RunState = task.RunStateRunning
				schedulerLock.Unlock() // unlock before resuming, Pause() will lock again
				sleepingTask.Resume()
				setCurrentTask(nil)
				continue
			}

			// It is not ready to run, so sleep until it is.
			delay := timeUnit(sleepingTask.Data) - now

			// Sleep for a bit until the next task is ready to run.
			schedulerIsRunning = true
			schedulerLock.Unlock()
			sleepTicks(delay)
			schedulerLock.Lock()
			schedulerIsRunning = false
			continue
		}

		// No runnable tasks and no sleeping tasks. There's nothing to do.
		// Wait until something happens (like an interrupt).
		// TODO: check for deadlocks.
		schedulerUnlockAndWait()
	}
}

func currentTask() *task.Task {
	return cpuTasks[currentCPU()]
}

func setCurrentTask(task *task.Task) {
	cpuTasks[currentCPU()] = task
}

func lockScheduler() {
	schedulerLock.Lock()
}

func unlockScheduler() {
	schedulerLock.Unlock()
}

func lockFutex() interrupt.State {
	mask := interrupt.Disable()
	futexLock.Lock()
	return mask
}

func unlockFutex(state interrupt.State) {
	futexLock.Unlock()
	interrupt.Restore(state)
}

// Use a single spinlock for atomics. This works fine, since atomics are very
// short sequences of instructions.
func lockAtomics() interrupt.State {
	mask := interrupt.Disable()
	atomicsLock.Lock()
	return mask
}

func unlockAtomics(mask interrupt.State) {
	atomicsLock.Unlock()
	interrupt.Restore(mask)
}

var systemStack [numCPU]uintptr

// Implementation detail of the internal/task package.
// It needs to store the system stack pointer somewhere, and needs to know how
// many cores there are to do so. But it doesn't know the number of cores. Hence
// why this is implemented in the runtime.
func systemStackPtr() *uintptr {
	return &systemStack[currentCPU()]
}
