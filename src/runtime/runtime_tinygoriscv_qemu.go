//go:build tinygo.riscv && virt && qemu

package runtime

import (
	"device/riscv"
	"math/bits"
	"runtime/volatile"
	"sync/atomic"
	"unsafe"
)

// This file implements the VirtIO RISC-V interface implemented in QEMU, which
// is an interface designed for emulation.

// One tick is 100ns by default in QEMU.
// (This is not a standard, just the default used by QEMU).
type timeUnit int64

const numCPU = 4

//export main
func main() {
	// Set the interrupt address.
	// Note that this address must be aligned specially, otherwise the MODE bits
	// of MTVEC won't be zero.
	riscv.MTVEC.Set(uintptr(unsafe.Pointer(&handleInterruptASM)))

	// Enable software interrupts. We'll need them to wake up other cores.
	riscv.MIE.SetBits(riscv.MIE_MSIE)

	// If we're not hart 0, wait until we get the signal everything has been set
	// up.
	if hartID := riscv.MHARTID.Get(); hartID != 0 {
		// Wait until we get the signal this hart is ready to start.
		// Note that interrupts are disabled, which means that the interrupt
		// isn't actually taken. But we can still wait for it using wfi.
		for riscv.MIP.Get()&riscv.MIP_MSIP == 0 {
			riscv.Asm("wfi")
		}

		// Clear the software interrupt.
		aclintMSWI.MSIP[hartID].Set(0)

		// Now that we've cleared the software interrupt, we can enable
		// interrupts as was already done on hart 0.
		riscv.MSTATUS.SetBits(riscv.MSTATUS_MIE)

		// Now start running the scheduler on this core.
		schedulerLock.Lock()
		scheduler(false)

		// The scheduler exited, which means main returned and the program
		// should exit.
		// Make sure hart 0 is woken (it might be asleep at the moment).
		if sleepingHarts&0b1 != 0 {
			// Hart 0 is currently sleeping, wake it up.
			sleepingHarts &^= 0b1 // clear the bit
			aclintMSWI.MSIP[0].Set(1)
		}

		// Make sure hart 0 can actually enter the scheduler (since we still
		// have the scheduler lock) to realize the program has exited.
		schedulerLock.Unlock()

		// Now wait until the program exits. This shouldn't take very long.
		for {
			riscv.Asm("wfi")
		}
	}

	// Enable global interrupts now that they've been set up.
	// This is currently only for timer interrupts.
	riscv.MSTATUS.SetBits(riscv.MSTATUS_MIE)

	// Set all MTIMECMP registers to a value that clears the MTIP bit in MIP.
	// If we don't do this, the wfi instruction won't work as expected.
	for i := 0; i < numCPU; i++ {
		aclintMTIMECMP[i].Set(0xffff_ffff_ffff_ffff)
	}

	run()
	exit(0)
}

//go:extern handleInterruptASM
var handleInterruptASM [0]uintptr

//export handleInterrupt
func handleInterrupt() {
	cause := riscv.MCAUSE.Get()
	code := uint(cause &^ (1 << 31))
	if cause&(1<<31) != 0 {
		// Topmost bit is set, which means that it is an interrupt.
		switch code {
		// Note: software and timer interrupts are handled by disabling
		// interrupts and waiting for the corresponding bit in MIP to change.
		// (This is to avoid TOCTOU issues between checking for a flag and the
		// wfi instruction).
		default:
			print("fatal error: unknown interrupt")
			abort()
		}
	} else {
		// Topmost bit is clear, so it is an exception of some sort.
		// We could implement support for unsupported instructions here (such as
		// misaligned loads). However, for now we'll just print a fatal error.
		handleException(code)
	}

	// Zero MCAUSE so that it can later be used to see whether we're in an
	// interrupt or not.
	riscv.MCAUSE.Set(0)
}

func ticksToNanoseconds(ticks timeUnit) int64 {
	return int64(ticks) * 100 // one tick is 100ns
}

func nanosecondsToTicks(ns int64) timeUnit {
	return timeUnit(ns / 100) // one tick is 100ns
}

func sleepTicks(d timeUnit) {
	// Disable all interrupts.
	riscv.MSTATUS.ClearBits(riscv.MSTATUS_MIE)

	// Configure timeout.
	target := uint64(ticks() + d)
	hartID := riscv.MHARTID.Get()
	aclintMTIMECMP[hartID].Set(target)

	// Wait until the timeout is hit.
	riscv.MIE.SetBits(riscv.MIE_MTIE)
	for riscv.MIP.Get()&riscv.MIP_MTIP == 0 {
		riscv.Asm("wfi")
	}
	riscv.MIE.ClearBits(riscv.MIE_MTIE)

	// Set MTIMECMP to a high value so that MTIP goes low.
	aclintMTIMECMP[hartID].Set(0xffff_ffff_ffff_ffff)

	// Re-enable all interrupts.
	riscv.MSTATUS.SetBits(riscv.MSTATUS_MIE)
}

func ticks() timeUnit {
	// Combining the low bits and the high bits (at a rate of 100ns per tick)
	// yields a time span of over 59930 years without counter rollover.
	highBits := aclintMTIME.high.Get()
	for {
		lowBits := aclintMTIME.low.Get()
		newHighBits := aclintMTIME.high.Get()
		if newHighBits == highBits {
			// High bits stayed the same.
			return timeUnit(lowBits) | (timeUnit(highBits) << 32)
		}
		// Retry, because there was a rollover in the low bits (happening every
		// ~7 days).
		highBits = newHighBits
	}
}

// Memory-mapped I/O as defined by QEMU.
// Source: https://github.com/qemu/qemu/blob/master/hw/riscv/virt.c
// Technically this is an implementation detail but hopefully they won't change
// the memory-mapped I/O registers.
var (
	// UART0 output register.
	stdoutWrite = (*volatile.Register8)(unsafe.Pointer(uintptr(0x10000000)))
	// SiFive test finisher
	testFinisher = (*volatile.Register32)(unsafe.Pointer(uintptr(0x100000)))

	// RISC-V Advanced Core Local Interruptor.
	// It is backwards compatible with the SiFive CLINT.
	// https://github.com/riscvarchive/riscv-aclint/blob/main/riscv-aclint.adoc
	aclintMTIME = (*struct {
		low  volatile.Register32
		high volatile.Register32
	})(unsafe.Pointer(uintptr(0x0200_bff8)))
	aclintMTIMECMP = (*[4095]volatile.Register64)(unsafe.Pointer(uintptr(0x0200_4000)))
	aclintMSWI     = (*struct {
		MSIP [4095]volatile.Register32
	})(unsafe.Pointer(uintptr(0x0200_0000)))
)

func putchar(c byte) {
	stdoutWrite.Set(uint8(c))
}

func getchar() byte {
	// dummy, TODO
	return 0
}

func buffered() int {
	// dummy, TODO
	return 0
}

// Define the various spinlocks needed by the runtime.
var (
	schedulerLock spinLock
	futexLock     spinLock
	atomicsLock   spinLock
)

type spinLock struct {
	atomic.Uint32
}

func (l *spinLock) Lock() {
	// Try to replace 0 with 1. Once we succeed, the lock has been acquired.
	for !l.Uint32.CompareAndSwap(0, 1) {
		// Hint to the CPU that this core is just waiting, and the core can go
		// into a lower energy state.
		// This is a no-op in QEMU TCG (but added here for completeness):
		// https://github.com/qemu/qemu/blob/v9.2.3/target/riscv/insn_trans/trans_rvi.c.inc#L856
		riscv.Asm("pause")
	}
}

func (l *spinLock) Unlock() {
	// Safety check: the spinlock should have been locked.
	if schedulerAsserts && l.Uint32.Load() != 1 {
		runtimePanic("unlock of unlocked spinlock")
	}

	// Unlock the lock. Simply write 0, because we already know it is locked.
	l.Uint32.Store(0)
}

func currentCPU() uint32 {
	return uint32(riscv.MHARTID.Get())
}

func startSecondaryCores() {
	// Start all the other cores besides hart 0.
	for hart := 1; hart < numCPU; hart++ {
		// Signal the given hart it is ready to start using a software
		// interrupt.
		aclintMSWI.MSIP[hart].Set(1)
	}
}

// Bitset of harts that are currently sleeping in schedulerUnlockAndWait.
// This supports up to 8 harts.
// This variable may only be accessed with the scheduler lock held.
var sleepingHarts uint8

// Put the scheduler to sleep, since there are no tasks to run.
// This will unlock the scheduler lock, and must be called with the scheduler
// lock held.
func schedulerUnlockAndWait() {
	hartID := riscv.MHARTID.Get()

	// Mark the current hart as sleeping.
	sleepingHarts |= uint8(1 << hartID)

	// Wait for a software interrupt, with interrupts disabled and the scheduler
	// unlocked.
	riscv.MSTATUS.ClearBits(riscv.MSTATUS_MIE)
	schedulerLock.Unlock()
	for riscv.MIP.Get()&riscv.MIP_MSIP == 0 {
		riscv.Asm("wfi")
	}
	aclintMSWI.MSIP[hartID].Set(0)
	schedulerLock.Lock()
	riscv.MSTATUS.SetBits(riscv.MSTATUS_MIE)
}

// Wake another core, if one is sleeping. Must be called with the scheduler lock
// held.
func schedulerWake() {
	// Look up the lowest-numbered hart that is sleeping.
	// Returns 8 if there are no sleeping harts.
	hart := bits.TrailingZeros8(sleepingHarts)

	if hart < 8 {
		// There is a sleeping hart. Wake it.
		sleepingHarts &^= 1 << hart  // clear the bit
		aclintMSWI.MSIP[hart].Set(1) // send software interrupt
	}
}

func abort() {
	exit(1)
}

func exit(code int) {
	// Make sure the QEMU process exits.
	if code == 0 {
		testFinisher.Set(0x5555) // FINISHER_PASS
	} else {
		// Exit code is stored in the upper 16 bits of the 32 bit value.
		testFinisher.Set(uint32(code)<<16 | 0x3333) // FINISHER_FAIL
	}

	// Lock up forever (as a fallback).
	for {
		riscv.Asm("wfi")
	}
}

// handleException is called from the interrupt handler for any exception.
// Exceptions can be things like illegal instructions, invalid memory
// read/write, and similar issues.
func handleException(code uint) {
	// For a list of exception codes, see:
	// https://content.riscv.org/wp-content/uploads/2019/08/riscv-privileged-20190608-1.pdf#page=49
	print("fatal error: exception with mcause=")
	print(code)
	print(" pc=")
	print(riscv.MEPC.Get())
	println()
	abort()
}
