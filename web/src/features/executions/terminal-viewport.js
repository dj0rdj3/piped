// Each output buffer owns its reading position. Rendering may follow the bottom,
// but must never restore an old scroll offset over a newer user scroll event.
export function createTerminalViewport({ element, nextTick, onFollowing }) {
  let active;
  let revision = 0;
  let restoring = false;

  function position(buffer) {
    return (buffer.viewport ||= { top: 0, left: 0, following: true });
  }

  function save() {
    const terminal = element();
    if (active && terminal && !restoring) {
      position(active).top = terminal.scrollTop;
      position(active).left = terminal.scrollLeft;
    }
  }

  async function activate(buffer) {
    if (active === buffer) {
      return;
    }
    save();
    active = buffer;
    const current = ++revision;
    restoring = true;
    onFollowing(buffer ? position(buffer).following : true);
    await nextTick();
    if (current !== revision) {
      return;
    }
    const terminal = element();
    if (buffer && terminal) {
      const view = position(buffer);
      terminal.scrollTop = view.following ? terminal.scrollHeight : view.top;
      terminal.scrollLeft = view.left;
    }
    restoring = false;
  }

  async function rendered(buffer) {
    const current = revision;
    await nextTick();
    if (active !== buffer || current !== revision || restoring) {
      return;
    }
    const terminal = element();
    if (terminal && position(buffer).following) {
      terminal.scrollTop = terminal.scrollHeight;
    }
    save();
  }

  return {
    activate,
    rendered,
    pause() {
      if (active) {
        position(active).following = false;
        onFollowing(false);
        save();
      }
    },
    scroll() {
      const terminal = element();
      if (!active || !terminal || restoring) {
        return;
      }
      const following = terminal.scrollHeight - terminal.scrollTop - terminal.clientHeight < 2;
      position(active).following = following;
      onFollowing(following);
      save();
    },
    follow() {
      if (active) {
        position(active).following = true;
        onFollowing(true);
      }
    },
  };
}
