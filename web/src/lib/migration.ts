import type { Api, StaleFile } from './files';

export interface MigrationIO {
  resolve(name: string, choice: 'default' | 'mine', text: string, baseHash: string): Promise<Api<{ files: StaleFile[] }>>;
}

export const CHANGED_ELSEWHERE = 'Changed on disk: the file was reloaded and your text is kept. Save again to replace it.';

// MigrationFlow is the split view of spec 5d §4.4: the user's file on the
// left (editable), the new default on the right. Every answer carries the
// files still stale, and that list — not a local count — drives the view.
// An edited left side is a draft kept per file, so Next and Previous never
// drop it; dirty is "some draft exists".
export class MigrationFlow {
  idx = 0;
  mine = '';
  error = '';
  busy = false;
  linkDown = false;
  onChange: () => void = () => {};
  onDone: () => void = () => {};
  private drafts: Record<string, string> = {};

  constructor(
    public files: StaleFile[],
    private readonly io: MigrationIO,
    public viewOnly: boolean,
  ) {
    this.mine = files[0]?.mine ?? '';
  }

  get dirty(): boolean {
    return Object.keys(this.drafts).length > 0;
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
    const cur = this.current();
    if (!cur || this.readOnly()) return;
    this.mine = t;
    this.drafts = { ...this.drafts, [cur.name]: t };
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
    this.show();
    this.error = '';
    this.onChange();
  }

  // show puts the current file's draft, or its text on disk, on the left.
  private show(): void {
    const cur = this.current();
    this.mine = cur ? (this.drafts[cur.name] ?? cur.mine) : '';
  }

  private async resolve(choice: 'default' | 'mine'): Promise<void> {
    const cur = this.current();
    if (!cur || this.readOnly()) return;
    this.busy = true;
    this.error = '';
    this.onChange();
    const r = await this.io.resolve(cur.name, choice, choice === 'mine' ? this.mine : '', cur.hash);
    this.busy = false;
    if (!r.ok) {
      if (r.status === 409 && r.current) {
        // Another save landed meanwhile: the list entry takes the file as it
        // is now, the draft stays, and the next save carries the new hash.
        const now = r.current;
        this.files = this.files.map((f) => (f.name === cur.name ? { ...f, mine: now.text, hash: now.hash } : f));
        this.show();
        this.error = CHANGED_ELSEWHERE;
      } else {
        this.error = r.error;
      }
      this.onChange();
      return;
    }
    const { [cur.name]: _saved, ...rest } = this.drafts;
    this.drafts = rest;
    this.files = r.value.files;
    this.idx = Math.min(this.idx, Math.max(0, this.files.length - 1));
    this.show();
    this.onChange();
    if (this.done()) this.onDone();
  }
}
