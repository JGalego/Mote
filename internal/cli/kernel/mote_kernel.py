"""A Jupyter kernel for mote.

Every cell is one mote task or pipeline, written the way `mote pipe` takes
it, or `name = pipeline` to keep its output as {{name}} for later cells:

    chat "what's the capital of France"
    city = chat "name a landmark in the capital of France"
    chat "how tall is it? {{city}}"

A cell runs `mote nb exec`. What cells bind lives in a state file that
belongs to this kernel and goes away with it, so restarting the kernel starts
a session over. Models stay loaded between cells in mote's resident server.

Cells are the ones you type, so they run without asking first, as they would
in any shell. Needs ipykernel; MOTE_BIN names the mote to run.
"""
import base64
import os
import shutil
import subprocess
import tempfile

IMAGE_TYPES = {
    ".png": "image/png",
    ".jpg": "image/jpeg",
    ".jpeg": "image/jpeg",
    ".gif": "image/gif",
}


def run_cell(argv, code, state, cwd=None):
    """Run one cell with `mote nb exec`; return (ok, stdout, stderr).

    argv is the command that starts mote, as a list.
    """
    proc = subprocess.run(
        list(argv) + ["nb", "exec", "--state", state, "--yes"],
        input=code,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
        cwd=cwd,
    )
    return proc.returncode == 0, proc.stdout, proc.stderr


def image_paths(text):
    """The paths in text, when every line of it names an image that exists.

    Tasks that make pictures (draw, frames) print the files they wrote, one
    per line, and a notebook should show those rather than the paths.
    """
    lines = [line.strip() for line in text.splitlines() if line.strip()]
    for line in lines:
        if os.path.splitext(line)[1].lower() not in IMAGE_TYPES or not os.path.isfile(line):
            return []
    return lines


def image_bundle(path):
    """The mime bundle that displays the image at path."""
    with open(path, "rb") as f:
        data = base64.b64encode(f.read()).decode("ascii")
    return {IMAGE_TYPES[os.path.splitext(path)[1].lower()]: data, "text/plain": path}


def main():
    from ipykernel.kernelapp import IPKernelApp
    from ipykernel.kernelbase import Kernel

    argv = [os.environ.get("MOTE_BIN", "mote")]

    class MoteKernel(Kernel):
        implementation = "mote"
        implementation_version = "1"
        language_info = {
            "name": "mote",
            "mimetype": "text/x-sh",
            "file_extension": ".mote",
            "codemirror_mode": "shell",
        }
        banner = "mote: small models, local machines, useful work"

        def __init__(self, **kwargs):
            super().__init__(**kwargs)
            self._dir = tempfile.mkdtemp(prefix="mote-kernel-")
            self._state = os.path.join(self._dir, "state.json")

        def do_execute(self, code, silent, store_history=True, user_expressions=None, allow_stdin=False):
            try:
                ok, out, err = run_cell(argv, code, self._state)
            except KeyboardInterrupt:
                ok, out, err = False, "", "interrupted"
            reply = {"execution_count": self.execution_count}
            if ok:
                if not silent:
                    self._show(out)
                return dict(reply, status="ok", payload=[], user_expressions={})
            message = err.strip() or "mote failed"
            error = {"ename": "MoteError", "evalue": message, "traceback": [message]}
            if not silent:
                self.send_response(self.iopub_socket, "error", error)
            return dict(reply, status="error", **error)

        def _show(self, out):
            images = image_paths(out)
            if images:
                for path in images:
                    self.send_response(
                        self.iopub_socket,
                        "display_data",
                        {"data": image_bundle(path), "metadata": {}},
                    )
            elif out:
                self.send_response(self.iopub_socket, "stream", {"name": "stdout", "text": out})

        def do_shutdown(self, restart):
            shutil.rmtree(self._dir, ignore_errors=True)
            return super().do_shutdown(restart)

    IPKernelApp.launch_instance(kernel_class=MoteKernel)


if __name__ == "__main__":
    main()
