import { type Api, FILE_MAX_BYTES, type FileTarget, type TextFile } from './files';

export interface EditorIO {
  read(): Promise<Api<TextFile>>;
  write(text: string, baseHash: string, force: boolean): Promise<Api<TextFile>>;
}

const encoder = new TextEncoder();

// EditorSession is one open templates or plugin editor (spec 5d §4.2, §4.3).
// Like the notes editor it is not a Panel: it survives a lost link with its
// text, read-only until the link is back. The hash it sends is the one the
// gateway gave it; the page never hashes.
export class EditorSession {
  text = '';
  hash = '';
  exists = true;
  defaultText = '';
  loading = true;
  loadError = '';
  dirty = false;
  saving = false;
  conflict: TextFile | null = null;
  saveError = '';
  linkDown = false;
  onChange: () => void = () => {};
  onSaved: () => void = () => {};

  constructor(
    readonly target: FileTarget,
    readonly title: string,
    private readonly io: EditorIO,
    public viewOnly: boolean,
  ) {}

  readOnly(): boolean {
    return this.viewOnly || this.loading || this.loadError !== '' || this.linkDown;
  }

  async load(): Promise<void> {
    this.loading = true;
    this.onChange();
    const r = await this.io.read();
    this.loading = false;
    if (r.ok) {
      this.take(r.value);
      this.loadError = '';
    } else {
      this.loadError = r.error;
    }
    this.onChange();
  }

  edit(t: string): void {
    if (this.readOnly() || t === this.text) return;
    this.text = t;
    this.dirty = true;
    this.onChange();
  }

  // restoreDefault fills the box with the shipped file; nothing is written
  // until Save.
  restoreDefault(): void {
    if (this.defaultText !== '') this.edit(this.defaultText);
  }

  // save is Save / Ctrl+S; force is Overwrite after a conflict.
  async save(force = false): Promise<void> {
    if (this.readOnly() || this.saving || (!this.dirty && !force)) return;
    if (encoder.encode(this.text).length > FILE_MAX_BYTES) {
      this.saveError = 'Too large (max 256 KiB).';
      this.onChange();
      return;
    }
    const base = force && this.conflict ? this.conflict.hash : this.hash;
    this.saving = true;
    this.saveError = '';
    this.onChange();
    const r = await this.io.write(this.text, base, force);
    this.saving = false;
    if (r.ok) {
      this.take(r.value);
      this.conflict = null;
      this.onSaved();
    } else if (r.status === 409 && r.current) {
      this.conflict = r.current;
      this.saveError = 'Changed on disk.';
    } else {
      this.saveError = r.error;
    }
    this.onChange();
  }

  // takeTheirs is Reload after a conflict: the file on disk replaces the box.
  takeTheirs(): void {
    if (!this.conflict) return;
    this.take(this.conflict);
    this.conflict = null;
    this.saveError = '';
    this.onChange();
  }

  linkLost(): void {
    this.linkDown = true;
    this.onChange();
  }

  linkBack(): void {
    if (!this.linkDown) return;
    this.linkDown = false;
    this.onChange();
  }

  setViewOnly(v: boolean): void {
    if (v === this.viewOnly) return;
    this.viewOnly = v;
    this.onChange();
  }

  // close: 'ask' while there is unsaved text (the page confirms a discard).
  close(): 'closed' | 'ask' {
    return this.dirty ? 'ask' : 'closed';
  }

  private take(f: TextFile): void {
    this.text = f.text;
    this.hash = f.hash;
    this.exists = f.exists;
    if (f.default !== undefined) this.defaultText = f.default;
    this.dirty = false;
  }
}
