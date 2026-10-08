// Resizable columns and a menu that can be hidden. The widths and the
// hidden state are kept per browser in localStorage. This file loads in
// <head> without defer, so a stored layout applies before the page is
// first drawn and never flashes. Handlers listen on the document, so they
// keep working when htmx replaces parts of the page.
(() => {
  'use strict';

  const root = document.documentElement;
  const storageKey = 'leanfeed.layout';
  const step = 16;          // pixels per arrow key press
  const splitterWidth = 5;  // matches .splitter in style.css
  const minEntryWidth = 250;

  const load = () => {
    try {
      return JSON.parse(localStorage.getItem(storageKey)) || {};
    } catch {
      return {};
    }
  };
  const save = () => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(state));
    } catch {
      // Storage can be unavailable, for example in private windows.
    }
  };
  const state = load(); // {sidebar: px, list: px, menuHidden: bool}

  const apply = () => {
    for (const name of ['sidebar', 'list']) {
      if (state[name]) {
        root.style.setProperty(`--${name}-width`, `${state[name]}px`);
      } else {
        root.style.removeProperty(`--${name}-width`);
      }
    }
    root.classList.toggle('menu-hidden', Boolean(state.menuHidden));
  };
  apply();

  const pane = name => document.getElementById(name === 'sidebar' ? 'sidebar' : 'list');
  const width = name => pane(name)?.getBoundingClientRect().width || 0;

  // The menu stays readable, and the entry keeps at least minEntryWidth.
  const limits = name => {
    if (name === 'sidebar') {
      return [160, 480];
    }
    const menu = state.menuHidden ? 0 : width('sidebar') + splitterWidth;
    return [224, window.innerWidth - menu - splitterWidth - minEntryWidth];
  };

  const setWidth = (name, px) => {
    const [min, max] = limits(name);
    state[name] = Math.round(Math.min(Math.max(px, min), Math.max(min, max)));
    apply();
  };

  const splitterOf = e => e.target.closest?.('.splitter');

  document.addEventListener('pointerdown', e => {
    const handle = splitterOf(e);
    if (!handle || e.button !== 0) {
      return;
    }
    e.preventDefault();
    const name = handle.dataset.resize;
    const startX = e.clientX;
    const start = width(name);
    handle.setPointerCapture(e.pointerId);
    handle.classList.add('dragging');
    const move = ev => setWidth(name, start + ev.clientX - startX);
    const end = () => {
      handle.removeEventListener('pointermove', move);
      handle.classList.remove('dragging');
      save();
    };
    handle.addEventListener('pointermove', move);
    handle.addEventListener('pointerup', end, {once: true});
    handle.addEventListener('pointercancel', end, {once: true});
  });

  // Double-click restores the default width.
  document.addEventListener('dblclick', e => {
    const handle = splitterOf(e);
    if (handle) {
      delete state[handle.dataset.resize];
      apply();
      save();
    }
  });

  document.addEventListener('keydown', e => {
    const handle = splitterOf(e);
    const delta = {ArrowLeft: -step, ArrowRight: step}[e.key];
    if (handle && delta) {
      e.preventDefault();
      setWidth(handle.dataset.resize, width(handle.dataset.resize) + delta);
      save();
    }
  });

  document.addEventListener('click', e => {
    if (e.target.closest?.('[data-menu-toggle]')) {
      state.menuHidden = !state.menuHidden;
      apply();
      save();
    }
  });

  // Full screen article. The state is a class on the article itself, so it
  // ends when htmx replaces the article, and it is never stored.
  const setExpanded = (entry, on) => {
    entry.classList.toggle('expanded', on);
    const button = entry.querySelector('[data-entry-expand]');
    button.setAttribute('aria-pressed', String(on));
    button.title = on ? 'Leave full screen (Esc)' : 'Full screen';
    button.textContent = on ? '⤡' : '⤢';
  };

  document.addEventListener('click', e => {
    const entry = e.target.closest?.('[data-entry-expand]')?.closest('#entry');
    if (entry) {
      setExpanded(entry, !entry.classList.contains('expanded'));
    }
  });

  document.addEventListener('keydown', e => {
    const entry = document.querySelector('#entry.expanded');
    if (e.key === 'Escape' && entry) {
      setExpanded(entry, false);
    }
  });

  // The list marks the row of the open entry. htmx swaps the entry and the
  // rows separately, so the mark is set again after every swap.
  const markOpenEntry = () => {
    const id = document.getElementById('entry')?.dataset.entryId;
    for (const row of document.querySelectorAll('#list .row')) {
      if (id && row.id === `entry-${id}`) {
        row.setAttribute('aria-current', 'true');
      } else {
        row.removeAttribute('aria-current');
      }
    }
  };
  document.addEventListener('DOMContentLoaded', markOpenEntry);
  document.addEventListener('htmx:afterSettle', markOpenEntry);
})();
