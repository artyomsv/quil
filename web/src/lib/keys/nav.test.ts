import { describe, expect, it } from 'vitest';
import { neighbour } from './nav';

// p1 | p2
// ---+---
// p3 | p2
const panes = [
  { id: 'p1', rect: { x: 0, y: 0, w: 0.5, h: 0.5 } },
  { id: 'p2', rect: { x: 0.5, y: 0, w: 0.5, h: 1 } },
  { id: 'p3', rect: { x: 0, y: 0.5, w: 0.5, h: 0.5 } },
];

describe('neighbour', () => {
  it('finds the pane in each direction', () => {
    expect(neighbour(panes, 'p1', 'right')).toBe('p2');
    expect(neighbour(panes, 'p1', 'down')).toBe('p3');
    expect(neighbour(panes, 'p3', 'up')).toBe('p1');
    expect(neighbour(panes, 'p2', 'left')).toBe('p1');
  });
  it('answers empty at an edge', () => {
    expect(neighbour(panes, 'p1', 'left')).toBe('');
    expect(neighbour(panes, 'p2', 'right')).toBe('');
  });
  it('answers empty for a pane that is not placed', () => {
    expect(neighbour(panes, 'gone', 'right')).toBe('');
  });
});
