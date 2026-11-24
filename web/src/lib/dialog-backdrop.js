// Native dialogs retarget backdrop events to the dialog itself. Require both
// ends of the gesture outside, so text selections dragged out stay open.
export function createBackdropDismissal(close) {
  let pressedOutside = false;

  function isOutside(event) {
    if (event.target !== event.currentTarget) {
      return false;
    }
    const bounds = event.currentTarget.getBoundingClientRect();
    return (
      event.clientX < bounds.left ||
      event.clientX > bounds.right ||
      event.clientY < bounds.top ||
      event.clientY > bounds.bottom
    );
  }

  return {
    pointerDown(event) {
      pressedOutside = isOutside(event);
    },
    pointerCancel() {
      pressedOutside = false;
    },
    click(event) {
      const dismiss = pressedOutside && isOutside(event);
      pressedOutside = false;
      if (dismiss) {
        close();
      }
    },
  };
}
