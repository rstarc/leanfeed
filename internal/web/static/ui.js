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
})();
