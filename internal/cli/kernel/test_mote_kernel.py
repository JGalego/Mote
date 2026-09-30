"""Tests for the parts of the kernel that do not need ipykernel."""
import base64
import os
import sys
import tempfile
import unittest

import mote_kernel

# A stand-in for mote: it records how it was called and echoes the cell, or
# fails when the cell says so.
FAKE_MOTE = """\
import sys
open(sys.argv[0] + ".args", "w").write(" ".join(sys.argv[1:]))
cell = sys.stdin.read()
if "fail" in cell:
    sys.stderr.write("mote: no such task\\n")
    sys.exit(2)
print("echo: " + cell)
"""

PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
)


class RunCell(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.fake = os.path.join(self.dir.name, "fake_mote.py")
        with open(self.fake, "w") as f:
            f.write(FAKE_MOTE)
        self.argv = [sys.executable, self.fake]
        self.state = os.path.join(self.dir.name, "state.json")

    def test_runs_nb_exec_with_the_cell_on_stdin(self):
        ok, out, err = mote_kernel.run_cell(self.argv, 'chat "héllo"', self.state)
        self.assertTrue(ok)
        self.assertEqual(out.strip(), 'echo: chat "héllo"')
        with open(self.fake + ".args") as f:
            self.assertEqual(f.read(), "nb exec --state %s --yes" % self.state)

    def test_reports_a_failed_cell(self):
        ok, out, err = mote_kernel.run_cell(self.argv, "fail", self.state)
        self.assertFalse(ok)
        self.assertEqual(out, "")
        self.assertIn("no such task", err)

    def test_runs_in_the_directory_it_is_given(self):
        # A cell's relative paths are the notebook's, so the working directory
        # is the caller's to choose.
        with open(os.path.join(self.dir.name, "cwd_mote.py"), "w") as f:
            f.write("import os\nprint(os.getcwd())\n")
        argv = [sys.executable, os.path.join(self.dir.name, "cwd_mote.py")]
        ok, out, _ = mote_kernel.run_cell(argv, "x", self.state, cwd=self.dir.name)
        self.assertTrue(ok)
        self.assertEqual(os.path.realpath(out.strip()), os.path.realpath(self.dir.name))


class Images(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)

    def path(self, name, data=PNG):
        p = os.path.join(self.dir.name, name)
        with open(p, "wb") as f:
            f.write(data)
        return p

    def test_a_list_of_image_files_is_shown_as_images(self):
        a, b = self.path("a.png"), self.path("b.JPG")
        self.assertEqual(mote_kernel.image_paths("%s\n\n%s\n" % (a, b)), [a, b])

    def test_anything_else_is_text(self):
        a = self.path("a.png")
        for text in ["", "\n", "Paris.", "%s\nand some words" % a, os.path.join(self.dir.name, "ghost.png"), self.path("a.txt")]:
            self.assertEqual(mote_kernel.image_paths(text), [], repr(text))

    def test_bundle_carries_the_bytes_and_the_path(self):
        a = self.path("a.png")
        bundle = mote_kernel.image_bundle(a)
        self.assertEqual(base64.b64decode(bundle["image/png"]), PNG)
        self.assertEqual(bundle["text/plain"], a)


class State(unittest.TestCase):
    def test_sweep_removes_only_the_folders_of_kernels_that_are_gone(self):
        with tempfile.TemporaryDirectory() as tmp:
            gone = os.path.join(tmp, "mote-kernel-999999999-abc")
            mine = os.path.join(tmp, "mote-kernel-%d-abc" % os.getpid())
            other = os.path.join(tmp, "unrelated-999999999")
            odd = os.path.join(tmp, "mote-kernel-abc")  # an older name, with no pid
            for d in (gone, mine, other, odd):
                os.mkdir(d)
            mote_kernel.sweep_state(tmp)
            self.assertFalse(os.path.exists(gone))
            for d in (mine, other, odd):
                self.assertTrue(os.path.exists(d), d)

    def test_alive(self):
        self.assertTrue(mote_kernel.alive(os.getpid()))
        self.assertFalse(mote_kernel.alive(999999999))

    def test_sweep_of_a_folder_that_is_not_there(self):
        mote_kernel.sweep_state("/nonexistent/for/sure")


if __name__ == "__main__":
    unittest.main()
