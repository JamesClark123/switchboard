#!/usr/bin/env python3
"""Faithfully reproduce ptySession: run a command under a host PTY master,
let it run, then SIGKILL the child and CLOSE the master (mimicking
ptySession.Close: cmd.Process.Kill() + f.Close()). This delivers a TTY
hangup exactly like a daemon restart/detach would.
"""
import os, pty, sys, time, signal, fcntl, termios, struct

argv = sys.argv[1:]
pid, master = pty.fork()
if pid == 0:
    # child: exec the command with a controlling TTY (the pty slave)
    os.execvp(argv[0], argv)
    os._exit(127)

# parent: set a window size on the master so -it is happy
winsz = struct.pack("HHHH", 40, 120, 0, 0)
fcntl.ioctl(master, termios.TIOCSWINSZ, winsz)

# drain output for ~4s so the container process starts and logs
end = time.time() + 4.0
os.set_blocking(master, False)
while time.time() < end:
    try:
        os.read(master, 4096)
    except (BlockingIOError, OSError):
        pass
    time.sleep(0.05)

# mimic ptySession.Close(): kill the sbx-exec client then close the PTY master
os.kill(pid, signal.SIGKILL)
os.close(master)          # <-- TTY hangup toward the slave side
print("HOST: killed sbx-exec client (pid %d) + closed pty master" % pid)
