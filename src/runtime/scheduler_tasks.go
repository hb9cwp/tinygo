//go:build scheduler.tasks

package runtime

import "internal/task"

// getSystemStackPointer returns the current stack pointer of the system stack.
// This is not necessarily the same as the current stack pointer.
func getSystemStackPointer() uintptr {
	// TODO: this always returns the correct stack on Cortex-M, so don't bother
	// comparing against 0.
	sp := task.SystemStack()
	if sp == 0 {
		sp = getCurrentStackPointer()
	}
	return sp
}

var systemStack uintptr

// Implementation detail of the internal/task package.
// It needs to store the system stack pointer somewhere, and needs to know how
// many cores there are to do so. But it doesn't know the number of cores. Hence
// why this is implemented in the runtime.
func systemStackPtr() *uintptr {
	return &systemStack
}
