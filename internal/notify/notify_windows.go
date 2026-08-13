//go:build windows

package notify

import (
	"time"

	"errors"
	"fmt"
	"github.com/artyomsv/quil/internal/logger"

	"runtime"
	"sync"
	"unsafe"
)

// notificationSetting values from Windows.UI.Notifications.NotificationSetting.
const (
	settingEnabled = 0
)

var settingNames = map[uint32]string{
	0: "Enabled",
	1: "DisabledForApplication",
	2: "DisabledForUser",
	3: "DisabledByGroupPolicy",
	4: "DisabledByManifest (AUMID not recognised)",
}

// winNotifier owns a dedicated OS thread and performs every COM call on it.
//
// This is not tidiness. COM apartment state is PER-THREAD, and a Go goroutine
// migrates between OS threads freely — so initialising COM in New and then
// calling from Bubble Tea's Update goroutine would issue WinRT calls on threads
// that never ran RoInitialize. That fails intermittently and environmentally,
// which is the worst possible shape for a bug in a process that runs for weeks.
//
// A single serving thread also makes the calls serial, so no mutex is needed,
// and it preserves the order jobs were queued in — which matters, because a
// Withdraw overtaking its own Notify would leave a toast in Action Center
// forever.
type winNotifier struct {
	opts Options
	work chan func()
	quit chan struct{}
	once sync.Once
}

// workQueueDepth bounds the queued-but-unrun jobs.
//
// Sized like Pane.EnqueueInput's queue and for the same reason: the producer
// is a goroutine that must never block, so the queue must be able to refuse.
// Generous relative to the real rate — a 30 s per-pane cooldown means a
// hundred panes cannot fill this — so overflow means the COM thread is wedged,
// where dropping is the only correct answer.
const workQueueDepth = 64

// syncCallTimeout bounds the CLI's synchronous path. Only `quil notify test`
// waits, and it is a one-shot command with nothing else to do — but it must
// still not hang a terminal forever on a wedged notification service.
const syncCallTimeout = 30 * time.Second

// New builds a toast notifier for this AUMID.
//
// Refuses up front when the AUMID has no backing Start Menu shortcut, rather
// than letting every Notify fail invisibly later: Show returns S_OK for an
// unregistered AUMID and simply displays nothing.
//
// The shortcut is the real precondition; see notifyLocked for why get_Setting
// is NOT one, despite looking like the obvious gate.
func New(opts Options) (Notifier, error) {
	ok, err := Registered(opts)
	if err != nil {
		return nil, fmt.Errorf("notify: checking registration: %w", err)
	}
	if !ok {
		return nil, ErrNotRegistered
	}

	w := &winNotifier{
		opts: opts,
		work: make(chan func(), workQueueDepth),
		quit: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go w.serve(ready)
	// Bounded: this runs during launch, before tea.NewProgram, so a hung
	// RoInitialize here would mean the TUI never starts at all.
	select {
	case err := <-ready:
		if err != nil {
			// w.Close(), not a bare close(w.quit): Close goes through the
			// sync.Once, so this cannot become a close-of-closed panic if a
			// later refactor ever returns a partially-initialised notifier.
			_ = w.Close()
			return nil, err
		}
	case <-time.After(syncCallTimeout):
		_ = w.Close()
		return nil, errors.New("notify: COM initialisation timed out")
	}
	return w, nil
}

// serve is the COM thread. Everything WinRT happens here and nowhere else.
func (w *winNotifier) serve(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	initCOMThisThread()
	// Declares which registered app this process is; see setProcessAUMID.
	if err := setProcessAUMID(w.opts.AUMID); err != nil {
		ready <- err
		return
	}
	ready <- nil

	for {
		select {
		case fn := <-w.work:
			fn()
		case <-w.quit:
			return
		}
	}
}

// enqueue hands fn to the COM thread and returns WITHOUT waiting.
//
// This is the whole point of the type: the caller is Bubble Tea's Update
// goroutine, and parking it on a cross-process WinRT round-trip freezes the
// entire TUI. A display failure is logged here rather than returned, because
// no caller on that path does anything with it but log.
//
// Drops on a full queue rather than blocking — the same choice
// Pane.EnqueueInput makes, for the same reason: a producer that cannot block
// must be able to refuse.
func (w *winNotifier) enqueue(what string, fn func() error) error {
	job := func() {
		if err := fn(); err != nil {
			logger.Debug("notify: %s failed: %v", what, err)
		}
	}
	select {
	case <-w.quit:
		return errors.New("notify: notifier closed")
	default:
	}
	select {
	case w.work <- job:
		return nil
	case <-w.quit:
		return errors.New("notify: notifier closed")
	default:
		return fmt.Errorf("notify: %s dropped, COM queue full", what)
	}
}

// doSync runs fn on the COM thread and waits, bounded.
func (w *winNotifier) doSync(fn func() error) error {
	errc := make(chan error, 1)
	select {
	case w.work <- func() { errc <- fn() }:
	case <-w.quit:
		return errors.New("notify: notifier closed")
	case <-time.After(syncCallTimeout):
		return errors.New("notify: COM queue busy")
	}
	select {
	case err := <-errc:
		return err
	case <-w.quit:
		return errors.New("notify: notifier closed")
	case <-time.After(syncCallTimeout):
		return errors.New("notify: COM call timed out")
	}
}

func (w *winNotifier) Notify(n Notification) error {
	return w.enqueue("toast", func() error { return w.notifyLocked(n) })
}

// NotifySync implements SyncNotifier — CLI only, never the TUI.
func (w *winNotifier) NotifySync(n Notification) error {
	return w.doSync(func() error { return w.notifyLocked(n) })
}

// WithdrawSync implements SyncNotifier — CLI only, never the TUI.
func (w *winNotifier) WithdrawSync(tag string) error {
	if tag == "" {
		return nil
	}
	return w.doSync(func() error { return w.withdrawLocked(tag) })
}

func (w *winNotifier) notifyLocked(n Notification) error {
	docClass, err := hstring("Windows.Data.Xml.Dom.XmlDocument")
	if err != nil {
		return err
	}
	defer freeHString(docClass)

	var doc uintptr
	r, _, _ := procRoActivateInstance.Call(docClass, uintptr(unsafe.Pointer(&doc)))
	if err := hrErr("RoActivateInstance(XmlDocument)", r); err != nil {
		return err
	}
	defer release(doc)

	docIO, err := queryInterface(doc, &iidXMLDocumentIO, "IXmlDocumentIO")
	if err != nil {
		return err
	}
	defer release(docIO)

	xmlH, err := hstring(BuildToastXML(n))
	if err != nil {
		return err
	}
	defer freeHString(xmlH)
	if err := hrErr("IXmlDocumentIO::LoadXml", call(docIO, idxXMLDocumentIOLoadXml, xmlH)); err != nil {
		return err
	}

	toastFactory, err := activationFactory("Windows.UI.Notifications.ToastNotification", &iidToastNotificationFactory)
	if err != nil {
		return err
	}
	defer release(toastFactory)

	var toast uintptr
	if err := hrErr("CreateToastNotification", call(toastFactory, idxToastFactoryCreate, doc, uintptr(unsafe.Pointer(&toast)))); err != nil {
		return err
	}
	defer release(toast)

	if err := w.tagToast(toast, n.Tag); err != nil {
		return err
	}

	statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastNotificationManagerStatics)
	if err != nil {
		return err
	}
	defer release(statics)

	aumidH, err := hstring(w.opts.AUMID)
	if err != nil {
		return err
	}
	defer freeHString(aumidH)

	var notifier uintptr
	if err := hrErr("CreateToastNotifierWithId", call(statics, idxToastStaticsNotifierWith, aumidH, uintptr(unsafe.Pointer(&notifier)))); err != nil {
		return err
	}
	defer release(notifier)

	// get_Setting is ADVISORY. It is not a precondition for Show, and treating
	// it as one was a real bug that made this feature look broken for a day.
	//
	// For an unpackaged app it answers ERROR_NOT_FOUND until Windows has an
	// entry for the AUMID in its notification settings database — something
	// that happens on Windows' own schedule and cannot be forced (a reboot, a
	// WpnUserService restart, and hand-writing both the AppUserModelId and
	// Notifications\Settings keys all failed to produce one). Meanwhile Show
	// displays the toast perfectly. Measured on 19045: with this call gating
	// the path, a correct registration reported failure for hours; with it
	// advisory, the same registration toasts immediately.
	//
	// A value that DOES come back is still worth acting on — DisabledForUser
	// or DisabledByGroupPolicy means the user or their admin turned this off,
	// and Show would be a silent no-op — so that case is still an error. Only
	// the "no entry yet" case is ignored.
	var setting uint32
	if err := hrErr("get_Setting", call(notifier, idxToastNotifierSetting, uintptr(unsafe.Pointer(&setting)))); err != nil {
		logger.Debug("notify: get_Setting unavailable for %s (%v); showing anyway", w.opts.AUMID, err)
	} else if setting != settingEnabled {
		name := settingNames[setting]
		if name == "" {
			name = "unknown"
		}
		return fmt.Errorf("notify: notifications are turned off for %s: %s", w.opts.AUMID, name)
	}

	return hrErr("IToastNotifier::Show", call(notifier, idxToastNotifierShow, toast))
}

// tagToast sets Tag and Group, then READS THE TAG BACK.
//
// The readback is not paranoia. IToastNotification2 declares put_ before get_,
// so a setter written at the get_ index returns S_OK and sets nothing — an
// HSTRING passed where an HSTRING* out-param is expected is accepted silently.
// The only downstream symptom is that Withdraw reports ERROR_NOT_FOUND much
// later, which is how the spike lost two runs. Verifying here turns a silent
// mis-index into an immediate, named error.
//
// Group is the AUMID so a dev instance and a prod instance can never withdraw
// each other's toasts.
func (w *winNotifier) tagToast(toast uintptr, tag string) error {
	if tag == "" {
		return nil
	}
	toast2, err := queryInterface(toast, &iidToastNotification2, "IToastNotification2")
	if err != nil {
		return err
	}
	defer release(toast2)

	tagH, err := hstring(tag)
	if err != nil {
		return err
	}
	defer freeHString(tagH)
	groupH, err := hstring(w.opts.AUMID)
	if err != nil {
		return err
	}
	defer freeHString(groupH)

	if err := hrErr("put_Tag", call(toast2, idxToastPutTag, tagH)); err != nil {
		return err
	}
	if err := hrErr("put_Group", call(toast2, idxToastPutGroup, groupH)); err != nil {
		return err
	}

	var got uintptr
	if err := hrErr("get_Tag", call(toast2, idxToastGetTag, uintptr(unsafe.Pointer(&got)))); err != nil {
		return err
	}
	if v := hstringValue(got); v != tag {
		return fmt.Errorf("notify: toast tag did not stick (got %q, want %q) — check the IToastNotification2 vtable indices", v, tag)
	}
	return nil
}

// Withdraw removes a toast from Action Center by tag.
//
// Without this, answering a prompt leaves a notification still claiming the
// pane needs attention — a surface that accumulates lies.
func (w *winNotifier) Withdraw(tag string) error {
	if tag == "" {
		return nil
	}
	return w.enqueue("withdraw", func() error { return w.withdrawLocked(tag) })
}

func (w *winNotifier) withdrawLocked(tag string) error {
	statics2, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastNotificationManagerStatics2)
	if err != nil {
		return err
	}
	defer release(statics2)

	var history uintptr
	if err := hrErr("get_History", call(statics2, idxToastStatics2History, uintptr(unsafe.Pointer(&history)))); err != nil {
		return err
	}
	defer release(history)

	tagH, err := hstring(tag)
	if err != nil {
		return err
	}
	defer freeHString(tagH)
	groupH, err := hstring(w.opts.AUMID)
	if err != nil {
		return err
	}
	defer freeHString(groupH)
	aumidH, err := hstring(w.opts.AUMID)
	if err != nil {
		return err
	}
	defer freeHString(aumidH)

	// The WithId variant, deliberately: the 2-arg RemoveGroupedTag resolves the
	// AUMID from the calling process, which for an unpackaged binary is not the
	// one registered — it removes nothing and returns ERROR_NOT_FOUND.
	return hrErr("RemoveGroupedTagWithId", call(history, idxHistoryRemoveGroupedTagWithID, tagH, groupH, aumidH))
}

// Close stops the COM thread. Idempotent — cmd/quil defers it and a caller may
// also close explicitly.
func (w *winNotifier) Close() error {
	w.once.Do(func() { close(w.quit) })
	return nil
}
