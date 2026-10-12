// A dialog of the main page in a window of its own. The main page opened this window and moves the
// dialog it built into it; the dialog's code stays there. Closing the window cancels the dialog.
dialogClose.onclick = () => window.close();
opener.dialogReady(window);
