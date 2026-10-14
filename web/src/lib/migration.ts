import type { Api, StaleFile } from './files';

export interface MigrationIO {
  resolve(name: string, choice: 'default' | 'mine', text: string): Promise<Api<{ files: StaleFile[] }>>;
}

// MigrationFlow is the split view of spec 5d §4.4: the user's file on the
// left (editable), the new default on the right. Every answer carries the
// files still stale, and that list — not a local count — drives the view.
export class MigrationFlow {
  idx = 0;
  mine = '';
  dirty = false;
  error = '';
  busy = false;
  linkDown = false;
  onChange: () => void = () => {};
  onDone: () => void = () => {};

  constructor(
    public files: StaleFile[],
    private readonly io: MigrationIO,
    public viewOnly: boolean,
  ) {
    this.mine = files[0]?.mine ?? '';
  }

  current(): StaleFile | null {
    return this.files[this.idx] ?? null;
  }

  done(): boolean {
    return this.files.length === 0;
  }

  readOnly(): boolean {
    return this.viewOnly || this.linkDown || this.busy;
  }

  edit(t: string): void {
    if (this.readOnly()) return;
    this.mine = t;
    this.dirty = true;
    this.onChange();
  }

  next(): void {
    this.move(this.idx + 1);
  }

  prev(): void {
    this.move(this.idx - 1);
  }

  useDefault(): Promise<void> {
    return this.resolve('default');
  }

  saveMine(): Promise<void> {
    return this.resolve('mine');
  }

  linkLost(): void {
    this.linkDown = true;
    this.onChange();
  }

  linkBack(): void {
    this.linkDown = false;
    this.onChange();
  }

  private move(i: number): void {
    const f = this.files[i];
    if (!f || i === this.idx) return;
    this.idx = i;
    this.mine = f.mine;
    this.dirty = false;
    this.error = '';
    this.onChange();
  }

  private async resolve(choice: 'default' | 'mine'): Promise<void> {
    const cur = this.current();
    if (!cur || this.readOnly()) return;
    this.busy = true;
    this.error = '';
    this.onChange();
    const r = await this.io.resolve(cur.name, choice, choice === 'mine' ? this.mine : '');
    this.busy = false;
    if (!r.ok) {
      this.error = r.error;
      this.onChange();
      return;
    }
    this.files = r.value.files;
    this.idx = Math.min(this.idx, Math.max(0, this.files.length - 1));
    this.mine = this.current()?.mine ?? '';
    this.dirty = false;
    this.onChange();
    if (this.done()) this.onDone();
  }
}
