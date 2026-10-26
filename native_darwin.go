//go:build darwin

package main

import (
	"errors"
	"log"
	"runtime"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// O app nativo precisa da thread principal para o AppKit.
func init() { runtime.LockOSThread() }

type nsPoint struct{ X, Y float64 }
type nsSize struct{ W, H float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}

var (
	winVisible atomic.Bool
	nsWin      objc.ID
	nsDelegate objc.ID
)

func sel(s string) objc.SEL { return objc.RegisterName(s) }
func cls(s string) objc.ID  { return objc.ID(objc.GetClass(s)) }
func nsStr(s string) objc.ID {
	return cls("NSString").Send(sel("stringWithUTF8String:"), s)
}

func menuItem(menu objc.ID, title string, action string, key string) objc.ID {
	it := cls("NSMenuItem").Send(sel("alloc")).Send(sel("initWithTitle:action:keyEquivalent:"), nsStr(title), sel(action), nsStr(key))
	menu.Send(sel("addItem:"), it)
	return it
}

func subMenu(bar objc.ID, title string) objc.ID {
	top := cls("NSMenuItem").Send(sel("new"))
	m := cls("NSMenu").Send(sel("alloc")).Send(sel("initWithTitle:"), nsStr(title))
	top.Send(sel("setSubmenu:"), m)
	bar.Send(sel("addItem:"), top)
	return m
}

// showWindowAsync traz a janela de volta (pode ser chamada de qualquer goroutine).
func showWindowAsync() {
	if nsDelegate != 0 {
		nsDelegate.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("djShow:"), objc.ID(0), false)
	}
}

func showWindow() {
	if nsWin == 0 {
		return
	}
	nsWin.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	cls("NSApplication").Send(sel("sharedApplication")).Send(sel("activateIgnoringOtherApps:"), true)
	winVisible.Store(true)
}

// runNative abre a janela nativa com a interface e roda o loop do AppKit (nao retorna).
// keepRunning diz se o app deve continuar aberto quando a janela fecha (downloads ativos).
func runNative(url string, keepRunning func() bool, onQuit func()) error {
	if _, err := purego.Dlopen("/System/Library/Frameworks/Cocoa.framework/Cocoa", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return err
	}
	if _, err := purego.Dlopen("/System/Library/Frameworks/WebKit.framework/WebKit", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		return err
	}
	if objc.GetClass("WKWebView") == 0 {
		return errors.New("WebKit indisponível")
	}
	delCls, err := objc.RegisterClass("DJAppDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) bool {
			winVisible.Store(false)
			return !keepRunning()
		}},
		{Cmd: sel("applicationShouldHandleReopen:hasVisibleWindows:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, _ bool) bool {
			showWindow()
			return true
		}},
		{Cmd: sel("applicationWillTerminate:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			onQuit()
		}},
		{Cmd: sel("djShow:"), Fn: func(self objc.ID, _ objc.SEL, _ objc.ID) {
			showWindow()
		}},
	})
	if err != nil {
		return err
	}
	app := cls("NSApplication").Send(sel("sharedApplication"))
	app.Send(sel("setActivationPolicy:"), int64(0))
	nsDelegate = objc.ID(delCls).Send(sel("new"))
	app.Send(sel("setDelegate:"), nsDelegate)

	// menus: sem o menu Editar, Cmd+C/Cmd+V nao funcionam no campo de link
	bar := cls("NSMenu").Send(sel("new"))
	am := subMenu(bar, "DriveCRTV")
	menuItem(am, "Ocultar DriveCRTV", "hide:", "h")
	menuItem(am, "Sair do DriveCRTV", "terminate:", "q")
	em := subMenu(bar, "Editar")
	menuItem(em, "Desfazer", "undo:", "z")
	menuItem(em, "Refazer", "redo:", "Z")
	menuItem(em, "Recortar", "cut:", "x")
	menuItem(em, "Copiar", "copy:", "c")
	menuItem(em, "Colar", "paste:", "v")
	menuItem(em, "Selecionar tudo", "selectAll:", "a")
	wm := subMenu(bar, "Janela")
	menuItem(wm, "Minimizar", "performMiniaturize:", "m")
	menuItem(wm, "Fechar", "performClose:", "w")
	app.Send(sel("setMainMenu:"), bar)

	rect := nsRect{Size: nsSize{1180, 800}}
	const style = 1 | 2 | 4 | 8 // titulo, fechar, minimizar, redimensionar
	nsWin = cls("NSWindow").Send(sel("alloc")).Send(sel("initWithContentRect:styleMask:backing:defer:"), rect, uint64(style), uint64(2), false)
	nsWin.Send(sel("setTitle:"), nsStr("DriveCRTV"))
	nsWin.Send(sel("setReleasedWhenClosed:"), false)
	nsWin.Send(sel("setMinSize:"), nsSize{720, 520})
	nsWin.Send(sel("center"))
	nsWin.Send(sel("setFrameAutosaveName:"), nsStr("DriveCRTVMain"))

	conf := cls("WKWebViewConfiguration").Send(sel("new"))
	wv := cls("WKWebView").Send(sel("alloc")).Send(sel("initWithFrame:configuration:"), rect, conf)
	wv.Send(sel("setAutoresizingMask:"), uint64(2|16))
	nsWin.Send(sel("setContentView:"), wv)
	req := cls("NSURLRequest").Send(sel("requestWithURL:"), cls("NSURL").Send(sel("URLWithString:"), nsStr(url)))
	wv.Send(sel("loadRequest:"), req)

	showWindow()
	log.Println("janela nativa aberta")
	app.Send(sel("run"))
	return nil
}
