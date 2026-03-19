#ifndef PTY_H
#define PTY_H

#include <stddef.h>

/*
 * pty_create — open a PTY master, grant/unlock it, and copy the slave
 * device path (e.g. /dev/ttys003 on macOS, /dev/pts/3 on Linux) into
 * slave_path.  Returns the master file-descriptor on success, -1 on error.
 */
int pty_create(char *slave_path, size_t path_size);

/*
 * pty_write — write len bytes of data to the PTY master fd.
 * Returns the number of bytes written, or -1 on error.
 */
int pty_write(int master_fd, const char *data, int len);

/*
 * pty_close — close the PTY master fd.
 */
void pty_close(int master_fd);

#endif /* PTY_H */
