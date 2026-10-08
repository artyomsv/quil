import type { Clock } from './connection';
import type { NoteResp, NoteSetResp } from './protocol';
import type { Outcome } from './requests';
import { sanitizeBlock, sanitizeRemoteText } from './sanitize';

export const NOTES_DEBOUNCE_MS = 30_000;
export const NOTE_LOAD_TIMEOUT_MS = 8000;
export const MAX_NOTE_BYTES = 256 * 1024;

const encoder = new TextEncoder();
export function noteBytes(s: string): number {
  return encoder.encode(s).length;
}

// saveText is TakeSave's text: one trailing newline on a non-empty note, and
// an emptied buffer as "" (the daemon's delete) — so the TUI and the browser
// store the same bytes for the same text.
export function saveText(raw: string): string {
  return raw !== '' && !raw.endsWith('\n') ? `${raw}\n` : raw;
}

export interface NoteIO {
  get(paneId: string): Promise<Outcome>;
  set(paneId: string, text: string, baseRev: number): Promise<Outcome>;
}

// NoteSession is one open notes editor: the TUI's NotesEditor plus the
// Model's note plumbing (internal/tui/notes.go, sharednotes.go), with one
// browser difference — a conflict offers "Copy my text" instead of writing
// notes-conflicts, so the editor stays open until its text is safe (R2).
export class NoteSession {
  text = '';
  loading = true;
  loadError = '';
  rev = 0;
  dirty = false;
  saving = false;
  conflict = false;
  currentRev = 0;
  saveError = '';
  hold = false;
  linkDown = false;
  paneGone = false;
  loadArmed = false;
  loadedBytes = 0;
  closing = false;
  onChange: () => void = () => {};
  onClosed: () => void = () => {};

  private inFlightText = '';
  private loadSeq = 0;
  // A note_get is out and unanswered: one at a time per editor.
  private getPending = false;
  private loadTimer: unknown = null;
  private saveTimer: unknown = null;
  // The confirmed reload ("Load theirs" twice): the buffer the user agreed
  // to discard, and how many saves were accepted at that moment
  // (noteLoadSnapshot / noteSavesTaken in the TUI).
  private discardSnapshot: string | null = null;
  private savesTaken = 0;
  private snapSaves = 0;
  private stopped = false;

  constructor(
    readonly paneId: string,
    private readonly io: NoteIO,
    private readonly clock: Clock,
    public viewOnly: boolean,
  ) {}

  load(): void {
    this.loading = true;
    this.loadError = '';
    this.sendGet(false);
    this.onChange();
  }

  edit(t: string): void {
    if (this.viewOnly || this.loading || this.loadError !== '' || this.linkDown || t === this.text) return;
    this.text = t;
    this.dirty = true;
    this.hold = false;
    this.armAutosave();
    this.onChange();
  }

  // tooLarge is the daemon's refusal (internal/daemon/notes.go): above the
  // cap AND larger than the stored note, so a note already above it can
  // still shrink.
  tooLarge(): boolean {
    const n = noteBytes(saveText(this.text));
    return n > MAX_NOTE_BYTES && n > this.loadedBytes;
  }

  // save is Ctrl+S (overwrite after a conflict) or autosave (plain).
  save(overwrite = false): void {
    if (this.viewOnly || this.loading || this.loadError !== '' || this.saving || !this.dirty || this.linkDown || this.paneGone) {
      return;
    }
    if (this.conflict && !overwrite) return;
    if (this.tooLarge()) {
      // The daemon would refuse it: a refusal like any other, so autosave
      // holds until the next edit and a closing editor offers Discard.
      this.saveError = 'too large';
      this.hold = true;
      this.clearAutosave();
      this.onChange();
      return;
    }
    const base = this.conflict ? this.currentRev : this.rev;
    const raw = this.text;
    this.saving = true;
    this.inFlightText = raw;
    this.clearAutosave();
    this.onChange();
    void this.io.set(this.paneId, saveText(raw), base).then((o) => this.saveAnswered(o));
  }

  // loadTheirs is Ctrl+R: the first call arms, the second sends the
  // confirmed reload. Only in a conflict and never under a save in flight.
  loadTheirs(): void {
    if (!this.conflict || this.saving) {
      this.loadArmed = false;
      this.onChange();
      return;
    }
    if (!this.loadArmed) {
      this.loadArmed = true;
      this.onChange();
      return;
    }
    this.loadArmed = false;
    this.sendGet(true);
    this.onChange();
  }

  // frameRev applies a state frame's note_rev (reconcileNoteRev): a higher
  // rev reloads a clean editor silently and marks a dirty one conflicted.
  frameRev(rev: number | undefined): void {
    if (rev === undefined || this.loading || this.saving || this.linkDown || rev <= this.rev) return;
    if (this.dirty) {
      this.markConflict(rev);
      this.onChange();
      return;
    }
    if (this.getPending) return;
    this.sendGet(false);
  }

  linkLost(): void {
    this.linkDown = true;
    // AbandonSave: no verdict will come; the text stays dirty and goes again
    // from the same base once the link is back.
    if (this.saving) {
      this.saving = false;
      this.inFlightText = '';
      this.saveError = 'connection lost';
    }
    this.loadSeq++;
    this.getPending = false;
    this.clearLoadTimer();
    this.clearAutosave();
    this.onChange();
  }

  linkBack(): void {
    if (!this.linkDown) return;
    this.linkDown = false;
    if (this.loading) this.sendGet(false);
    // A close that waited on the link saves at once and closes on its OK.
    else if (this.dirty && this.closing && !this.conflict && !this.hold) this.save();
    else if (this.dirty) this.armAutosave();
    this.onChange();
  }

  // setViewOnly follows a rights change across a reconnect: a session that
  // became read-only keeps its text but sends nothing more.
  setViewOnly(v: boolean): void {
    if (v === this.viewOnly) return;
    this.viewOnly = v;
    if (v) this.clearAutosave();
    else if (this.dirty) this.armAutosave();
    this.onChange();
  }

  paneClosed(): void {
    if (this.paneGone) return;
    this.paneGone = true;
    this.clearAutosave();
    this.onChange();
  }

  // close is the editor's Close/Escape: 'closed' when nothing is at risk,
  // else 'wait' (a save goes out and the editor closes on its OK).
  close(): 'closed' | 'wait' {
    if (this.viewOnly || this.loading || this.loadError !== '' || !this.dirty) {
      this.stop();
      return 'closed';
    }
    this.closing = true;
    // A closed pane's note cannot be saved: its text goes only by a
    // confirmed discard (or a copy first) — AC-18.
    if (!this.paneGone && !this.conflict && !this.hold && !this.saving) this.save();
    this.onChange();
    return 'wait';
  }

  stop(): void {
    this.stopped = true;
    this.clearAutosave();
    this.clearLoadTimer();
  }

  private sendGet(confirmed: boolean): void {
    const seq = ++this.loadSeq;
    this.getPending = true;
    this.discardSnapshot = confirmed ? this.text : null;
    this.snapSaves = this.savesTaken;
    this.clearLoadTimer();
    this.loadTimer = this.clock.setTimeout(() => {
      if (seq !== this.loadSeq) return;
      this.loadTimer = null;
      this.loadSeq++;
      this.getPending = false;
      if (this.loading) this.loadError = 'no answer from the daemon';
      this.onChange();
    }, NOTE_LOAD_TIMEOUT_MS);
    void this.io.get(this.paneId).then((o) => this.getAnswered(seq, confirmed, o));
  }

  private getAnswered(seq: number, confirmed: boolean, o: Outcome): void {
    if (this.stopped || seq !== this.loadSeq) return;
    this.getPending = false;
    this.clearLoadTimer();
    const snapshot = this.discardSnapshot;
    const savedSince = confirmed && this.savesTaken !== this.snapSaves;
    const discards = confirmed && !this.saving && this.text === snapshot && !savedSince;
    this.discardSnapshot = null;
    const p = (o.reply?.payload ?? null) as NoteResp | null;
    const err = !o.ok ? o.error : !p ? 'bad answer' : (p.error ?? '');
    if (err !== '') {
      // A reload that fails leaves the loaded text as it was; only a first
      // load has nothing to show.
      if (this.loading) this.loadError = sanitizeRemoteText(err);
      this.onChange();
      return;
    }
    const resp = p as NoteResp;
    // Revisions only grow on one daemon run: an answer older than the
    // editor is stale, except for the confirmed reload (a restored daemon
    // can hold a lower rev, and the user asked for its text).
    if (!this.loading && !discards && resp.rev < this.rev) {
      this.onChange();
      return;
    }
    // A reload that finds unsaved text (typed since, or a save in flight)
    // must not replace it: it is a conflict. Only the confirmed one discards.
    if (!this.loading && (this.dirty || this.saving) && !discards) {
      this.markConflict(resp.rev);
      this.onChange();
      return;
    }
    this.text = sanitizeBlock(resp.text);
    this.loadedBytes = noteBytes(this.text);
    this.rev = resp.rev;
    this.loading = false;
    this.loadError = '';
    this.dirty = false;
    this.conflict = false;
    this.loadArmed = false;
    this.saveError = '';
    this.hold = false;
    this.onChange();
  }

  private saveAnswered(o: Outcome): void {
    if (this.stopped || !this.saving) return;
    this.saving = false;
    const p = (o.reply?.payload ?? null) as NoteSetResp | null;
    if (o.ok && p?.ok) {
      this.savesTaken++;
      this.rev = p.rev ?? this.rev;
      if (this.text === this.inFlightText) this.dirty = false;
      this.conflict = false;
      this.loadArmed = false;
      this.saveError = '';
      this.hold = false;
      this.loadedBytes = Math.max(this.loadedBytes, noteBytes(saveText(this.inFlightText)));
    } else if (p?.conflict) {
      this.markConflict(p.current_rev ?? 0);
    } else if (p) {
      // A refusal with a verdict (too large, a write error): hold autosave
      // until the next edit — resending the same text cannot succeed.
      this.saveError = sanitizeRemoteText(p.error || (o.ok ? '' : o.error) || 'refused');
      this.hold = true;
    } else {
      // No verdict (timeout, link): the text stays dirty, no hold.
      this.saveError = o.ok ? '' : sanitizeRemoteText(o.error);
    }
    this.inFlightText = '';
    if (this.closing && !this.dirty) {
      this.stop();
      this.onClosed();
      return;
    }
    if (this.dirty && !this.conflict && !this.hold) this.armAutosave();
    this.onChange();
  }

  private markConflict(rev: number): void {
    if (this.conflict && this.currentRev > rev) rev = this.currentRev;
    this.conflict = true;
    this.currentRev = rev;
    this.loadArmed = false;
    this.clearAutosave();
  }

  private armAutosave(): void {
    this.clearAutosave();
    if (this.conflict || this.hold || this.viewOnly || this.paneGone) return;
    this.saveTimer = this.clock.setTimeout(() => {
      this.saveTimer = null;
      this.save();
    }, NOTES_DEBOUNCE_MS);
  }

  private clearAutosave(): void {
    if (this.saveTimer !== null) this.clock.clearTimeout(this.saveTimer);
    this.saveTimer = null;
  }

  private clearLoadTimer(): void {
    if (this.loadTimer !== null) this.clock.clearTimeout(this.loadTimer);
    this.loadTimer = null;
  }
}
