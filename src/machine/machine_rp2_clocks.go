//go:build rp2040 || rp2350

package machine

import (
	"device/arm"
	"device/rp"
	"runtime/volatile"
	"unsafe"
)

func CPUFrequency() uint32 {
	return cpuFreq
}

func SetCPUFrequency(cpuFreq uint64) uint32 {
	// XXX see line 203 in machine_rp2_clocks.go
	//  clkSys = pllSys (125MHz) / 1 = 125MHz
	//  csys := clks.clock(clkSys)
	// ...
	// Compute clock dividers
	// from init()
	//  https://github.com/tinygo-org/tinygo/blob/3e60eeb368f25f237a512e7553fd6d70f36dc74c/src/machine/machine_rp2_clocks.go#L180
	//      xosc   revdiv   REF    FBDIV   VCO     POSTDIV
	// pllSys: 12 / 1 =    12 MHz * 125 = 1500 MHZ / 6 / 2 = 125 MHz
	// pllUSB: 12 / 1 =    12 MHz * 40  =  480 MHz / 5 / 2 =  48 MHz
	//fb, revdiv, pd1, pd2, err := pllSearch{LockRefDiv: 1}.CalcDivs(xoscFreq*MHz, cpuFreq*MHz, MHz)
	fb, _, pd1, pd2, err := pllSearch{LockRefDiv: 1}.CalcDivs(xoscFreq*MHz, cpuFreq*MHz, MHz)
	if err != nil {
		panic(err)
	}
	pllsysFB, pllsysPD1, pllsysPD2 = uint32(fb), uint32(pd1), uint32(pd2)

	// from RPi SDK Repo: pll_init()
	//  https://github.com/raspberrypi/pico-sdk/blob/ee68c78d0afae2b69c03ae1a72bf5cc267a2d94c/src/rp2_common/hardware_pll/include/hardware/pll.h#L54
	// pll		pll_sys or pll_usb
 	// ref_div	Input clock divider
 	// vco_freq	Requested output from the VCO (voltage controlled oscillator)
 	// post_div1	Post Divider 1 - range 1-7. Must be >= post_div2
 	// post_div2	Post Divider 2 - range 1-7
	// see also pico-sdk/src/rp2_common/hardware_clocks/include/hardware/clocks.h
	//  https://github.com/raspberrypi/pico-sdk/blob/ee68c78d0afae2b69c03ae1a72bf5cc267a2d94c/src/rp2_common/hardware_clocks/include/hardware/clocks.h#L156C30-L156C36
	//pllsysFB, pllsysPD1, pllsysPD2 = 125, 6, 2	// 125 MHz  default PLL config on RP2040 "standard"
	//pllsysFB, pllsysPD1, pllsysPD2 = 125, 5, 2 	// 150 MHz  default PLL config on RP2350 "standard"
	//pllsysFB, pllsysPD1, pllsysPD2 =  73, 5, 1	// 175 MHz
	//pllsysFB, pllsysPD1, pllsysPD2 = 100, 6, 1	// 200 MHz  default PLL config on RP2040 "fast"
	//pllsysFB, pllsysPD1, pllsysPD2 = 125, 6, 1	// 250 MHz
	println("pllsysFB= ", pllsysFB, "   pllsysPD1= ", pllsysPD1, "   pllsysPD2= ", pllsysPD2)

	pllSys.init(1, pllsysFB, pllsysPD1, pllsysPD2)
	//return cpuFreq
	return xoscFreq / 1 * xoscFreq / pllsysPD1 / pllsysPD2
}

func FrequencyCount(cix uint32) uint32 {
	// takes the source as an argument and returns the frequency in kHz:
	// from RP2350 Datasheet, 8.1.5.2. Using the frequency counter
	// The SDK defines a frequency_count function in lines 147 - 174
	// https://github.com/raspberrypi/pico-sdk/blob/master/src/rp2_common/hardware_clocks/clocks.c

	//var fc0 := fc(src: cix)	// clock index		// see fc0 in struct clocksType of machine_rp2_2350.go#L80
	//	var clks *clocksType	// XXX
	//csys := clks.clock(clkSys)
	//var ct *rp.CLOCKS_Type		// tinygo/src/device/rp2350.go (auto generated)
	//var clks *clocksType	// XXX
	//ct := clks.fc0

	// If frequency counter is running need to wait for it. It runs even if the source is NULL
	// tinygo/src/device/rp2350.go
	// type CLOCKS_Type struct {
	//   FC0_STATUS           volatile.Register32 // 0xA4
	//
	//	for !clks.fc0.status.HasBits(0x1 << 4) {	// DONE: Test complete
	//		_ = tight_loop_contents(77, 123)
	//	}
//	for !ct.GetFC0_STATUS_DONE() {
//	for ct.GetFC0_STATUS_DONE() != 1 {
//	for !ct.GetFC0_STATUS_DONE() {
//	for !clocks.fc0.GetFC0_STATUS_DONE() {
	// func (o *CLOCKS_Type) GetFC0_STATUS_DONE() uint32 {		// from src/device/rp2350.go
        //   return (volatile.LoadUint32(&o.FC0_STATUS.Reg) & 0x10) >> 4
	// }
	//for !((volatile.LoadUint32(&clocks.fc0.status) & 0x10) >> 4) {
	//for !((clocks.fc0.status & 0x10) >> 4) {
	for !(clocks.fc0.status.HasBits(0x10)) {
		_ = tight_loop_contents(77, 123)
		//_ = tight_loop_contents(77, 123, "-")
	}

	// Set reference freq
	// fc->ref_khz = clock_get_hz(clk_ref) / 1000;
	//ct.SetFC0_REF_KHZ(12000000 / 1000) // XXX
	clocks.fc0.refKHz.Set(12000000 / 1000)

	// FIXME: Don't pick random interval. Use best interval
	// fc->interval = 10;
	// 1us * 2**interval with default interval = 8 gives a test interval of 250us
	// 4 Bit, see RP2350 Datasheet p. 543
	//ct.SetFC0_INTERVAL(10) 	// 2^10 * 1 us = 1 ms
	clocks.fc0.interval.Set(10)	// 2^10 * 1 us = 1 ms

	// XXX No min or max
	// fc->min_khz = 0;
	// fc->max_khz = 0xffffffff;	<= example differs from auto generated device/rp2350.go!
	// Minimum pass frequency in kHz. This is optional.
	// Set to 0 if you are not using the pass/fail flags
	//ct.SetFC0_MIN_KHZ(0)
	clocks.fc0.minKHz.Set(0)
	// Maximum pass frequency in kHz. This is optional.
	// Set to 0x1ffffff if you are not using the pass/fail flags
	//ct.SetFC0_MAX_KHZ(0x1ffffff)
	clocks.fc0.maxKHz.Set(0x1ffffff)

	// Set SRC which automatically starts the measurement
	// fc->src = src;
	// tinygo/src/device/rp2350.go
	// type CLOCKS_Type struct {
	//   FC0_SRC              volatile.Register32 // 0xA0
	//	clks.fc0.src = cix
	// Clock sent to frequency counter, set to 0 when not required
	// Writing to this register initiates the frequency count
	//ct.SetFC0_SRC(cix)
	clocks.fc0.src.Set(cix)

	// while(!(fc->status & CLOCKS_FC0_STATUS_DONE_BITS)) {
	//for ct.GetFC0_STATUS_DONE() != 1 {
	for !(clocks.fc0.status.HasBits(0x10)) {
		_ = tight_loop_contents(77, 123)
		//_ = tight_loop_contents(77, 123, "+")
	}

	// Return the result
	// return fc->result >> CLOCKS_FC0_RESULT_KHZ_LSB;
	//	return fc0.result
	//return ct.GetFC0_RESULT_KHZ()
	return (clocks.fc0.result.Get() & 0x3fffffe0) >> 5
}

func tight_loop_contents(a, b uint) uint {
//func tight_loop_contents(a, b uint, r rune) uint {
//func tight_loop_contents(a, b uint, s string) uint {
	// explanation:
	// https://forums.raspberrypi.com/viewtopic.php?t=349804
	// implementation from RP SDK:
	// https://github.com/raspberrypi/pico-sdk/blob/f396d05f8252d4670d4ea05c8b7ac938ef0cd381/src/rp2_common/pico_platform/include/pico/platform.h#L358
	// No-op function intended to be called by any tight hardware polling loop. Using this ubiquitously
	// makes it much easier to find tight loops, but also in the future \#ifdef-ed support for lockup
	// debugging might be added
	//
	// * This multiplies a by b using multiply instruction using the ARM mul instruction regardless of values (the compiler
	// * might otherwise choose to perform shifts/adds), i.e. this is a 1 cycle operation.
	// __force_inline static int32_t __mul_instruction(int32_t a, int32_t b) {
	//   asm ("mul %0, %1" : "+l" (a) : "l" (b) : );
	//   return a;		// returns (a * b)
	// }
	//print("w")
	//print(r)
	//print(s)
	// XXX inline assmbly for ARM, see tinygo/src/device/
	// https://tinygo.org/docs/concepts/compiler-internals/inline-assembly/
	return a * b
}

// clockIndex identifies a hardware clock
type clockIndex uint8

type clockType struct {
	ctrl     volatile.Register32
	div      volatile.Register32
	selected volatile.Register32
}

type fc struct {
	refKHz   volatile.Register32
	minKHz   volatile.Register32
	maxKHz   volatile.Register32
	delay    volatile.Register32
	interval volatile.Register32
	src      volatile.Register32
	status   volatile.Register32
	result   volatile.Register32
}

var clocks = (*clocksType)(unsafe.Pointer(rp.CLOCKS))

var configuredFreq [numClocks]uint32

type clock struct {
	*clockType
	cix clockIndex
}

// The delay in seconds for core voltage adjustments to
// settle. Taken from the Pico SDK.
const _VREG_VOLTAGE_AUTO_ADJUST_DELAY = 1 / 1e3

// clock returns the clock identified by cix.
func (clks *clocksType) clock(cix clockIndex) clock {
	return clock{
		&clks.clk[cix],
		cix,
	}
}

// hasGlitchlessMux returns true if clock contains a glitchless multiplexer.
//
// Clock muxing consists of two components:
//
// A glitchless mux, which can be switched freely, but whose inputs must be
// free-running.
//
// An auxiliary (glitchy) mux, whose output glitches when switched, but has
// no constraints on its inputs.
//
// Not all clocks have both types of mux.
func (clk *clock) hasGlitchlessMux() bool {
	return clk.cix == clkSys || clk.cix == clkRef
}

// configure configures the clock by selecting the main clock source src
// and the auxiliary clock source auxsrc
// and finally setting the clock frequency to freq
// given the input clock source frequency srcFreq.
func (clk *clock) configure(src, auxsrc, srcFreq, freq uint32) {
	if freq > srcFreq {
		panic("clock frequency cannot be greater than source frequency")
	}

	div := calcClockDiv(srcFreq, freq)

	// If increasing divisor, set divisor before source. Otherwise set source
	// before divisor. This avoids a momentary overspeed when e.g. switching
	// to a faster source and increasing divisor to compensate.
	if div > clk.div.Get() {
		clk.div.Set(div)
	}

	// If switching a glitchless slice (ref or sys) to an aux source, switch
	// away from aux *first* to avoid passing glitches when changing aux mux.
	// Assume (!!!) glitchless source 0 is no faster than the aux source.
	if clk.hasGlitchlessMux() && src == rp.CLOCKS_CLK_SYS_CTRL_SRC_CLKSRC_CLK_SYS_AUX {
		clk.ctrl.ClearBits(rp.CLOCKS_CLK_REF_CTRL_SRC_Msk)
		for !clk.selected.HasBits(1) {
		}
	} else
	// If no glitchless mux, cleanly stop the clock to avoid glitches
	// propagating when changing aux mux. Note it would be a really bad idea
	// to do this on one of the glitchless clocks (clkSys, clkRef).
	{
		// Disable clock. On clkRef and ClkSys this does nothing,
		// all other clocks have the ENABLE bit in the same position.
		clk.ctrl.ClearBits(rp.CLOCKS_CLK_GPOUT0_CTRL_ENABLE_Msk)
		if configuredFreq[clk.cix] > 0 {
			// Delay for 3 cycles of the target clock, for ENABLE propagation.
			// Note XOSC_COUNT is not helpful here because XOSC is not
			// necessarily running, nor is timer... so, 3 cycles per loop:
			delayCyc := configuredFreq[clkSys]/configuredFreq[clk.cix] + 1
			for delayCyc != 0 {
				// This could be done more efficiently but TinyGo inline
				// assembly is not yet capable enough to express that. In the
				// meantime, this forces at least 3 cycles per loop.
				delayCyc--
				arm.Asm("nop\nnop\nnop")
			}
		}
	}

	// Set aux mux first, and then glitchless mux if this clock has one.
	clk.ctrl.ReplaceBits(auxsrc<<rp.CLOCKS_CLK_SYS_CTRL_AUXSRC_Pos,
		rp.CLOCKS_CLK_SYS_CTRL_AUXSRC_Msk, 0)

	if clk.hasGlitchlessMux() {
		clk.ctrl.ReplaceBits(src<<rp.CLOCKS_CLK_REF_CTRL_SRC_Pos,
			rp.CLOCKS_CLK_REF_CTRL_SRC_Msk, 0)
		for !clk.selected.HasBits(1 << src) {
		}
	}

	// Enable clock. On clkRef and clkSys this does nothing,
	// all other clocks have the ENABLE bit in the same position.
	clk.ctrl.SetBits(rp.CLOCKS_CLK_GPOUT0_CTRL_ENABLE)

	// Now that the source is configured, we can trust that the user-supplied
	// divisor is a safe value.
	clk.div.Set(div)

	// Store the configured frequency
	configuredFreq[clk.cix] = freq

}

var pllsysFB, pllsysPD1, pllsysPD2 uint32

// Compute clock dividers.
//
// Note that the entire init function is computed at compile time
// by interp.
func init() {
	fb, _, pd1, pd2, err := pllSearch{LockRefDiv: 1}.CalcDivs(xoscFreq*MHz, cpuFreq, MHz)
	if err != nil {
		panic(err)
	}
	pllsysFB, pllsysPD1, pllsysPD2 = uint32(fb), uint32(pd1), uint32(pd2)
}

// init initializes the clock hardware.
//
// Must be called before any other clock function.
func (clks *clocksType) init() {
	// Start the watchdog tick
	Watchdog.startTick(xoscFreq)

	// Disable resus that may be enabled from previous software
	rp.CLOCKS.SetCLK_SYS_RESUS_CTRL_CLEAR(0)

	// Enable the xosc
	xosc.init()

	// Before we touch PLLs, switch sys and ref cleanly away from their aux sources.
	clks.clk[clkSys].ctrl.ClearBits(rp.CLOCKS_CLK_SYS_CTRL_SRC_Msk)
	for !clks.clk[clkSys].selected.HasBits(0x1) {
	}

	clks.clk[clkRef].ctrl.ClearBits(rp.CLOCKS_CLK_REF_CTRL_SRC_Msk)
	for !clks.clk[clkRef].selected.HasBits(0x1) {
	}

	// Configure PLLs
	//                   REF     FBDIV VCO            POSTDIV
	// pllSys: 12 / 1 = 12MHz * 125 = 1500MHZ / 6 / 2 = 125MHz
	// pllUSB: 12 / 1 = 12MHz * 40  = 480 MHz / 5 / 2 =  48MHz
	pllSys.init(1, pllsysFB, pllsysPD1, pllsysPD2)
	pllUSB.init(1, 40, 5, 2)

	// Configure clocks
	// clkRef = xosc (12MHz) / 1 = 12MHz
	cref := clks.clock(clkRef)
	cref.configure(rp.CLOCKS_CLK_REF_CTRL_SRC_XOSC_CLKSRC,
		0, // No aux mux
		xoscFreq,
		xoscFreq)

	if adjustCoreVoltage() {
		// Wait for the voltage to settle.
		const cycles = _VREG_VOLTAGE_AUTO_ADJUST_DELAY * xoscFreq * MHz
		for i := 0; i < cycles; i++ {
			arm.Asm("nop")
		}
	}

	// clkSys = pllSys (125MHz) / 1 = 125MHz
	csys := clks.clock(clkSys)
	csys.configure(rp.CLOCKS_CLK_SYS_CTRL_SRC_CLKSRC_CLK_SYS_AUX,
		rp.CLOCKS_CLK_SYS_CTRL_AUXSRC_CLKSRC_PLL_SYS,
		cpuFreq,
		cpuFreq)

	// clkUSB = pllUSB (48MHz) / 1 = 48MHz
	cusb := clks.clock(clkUSB)
	cusb.configure(0, // No GLMUX
		rp.CLOCKS_CLK_USB_CTRL_AUXSRC_CLKSRC_PLL_USB,
		48*MHz,
		48*MHz)

	// clkADC = pllUSB (48MHZ) / 1 = 48MHz
	cadc := clks.clock(clkADC)
	cadc.configure(0, // No GLMUX
		rp.CLOCKS_CLK_ADC_CTRL_AUXSRC_CLKSRC_PLL_USB,
		48*MHz,
		48*MHz)

	clks.initRTC()

	// clkPeri = clkSys. Used as reference clock for Peripherals.
	// No dividers so just select and enable.
	// Normally choose clkSys or clkUSB.
	cperi := clks.clock(clkPeri)
	cperi.configure(0,
		rp.CLOCKS_CLK_PERI_CTRL_AUXSRC_CLK_SYS,
		cpuFreq,
		cpuFreq)

	clks.initTicks()
}
