/*
 * pty.c — POSIX PTY helpers for fakegps.
 *
 * Compatible with macOS (Darwin/BSD) and Linux.
 * On macOS the slave appears as /dev/ttysNNN; on Linux as /dev/pts/N.
 */

/* Needed on Linux for posix_openpt / ptsname; harmless on macOS. */
#define _XOPEN_SOURCE 600
/* Needed on Linux for cfmakeraw; harmless on macOS. */
#define _DEFAULT_SOURCE
/* Needed on macOS to expose cfmakeraw when _XOPEN_SOURCE is set. */
#ifdef __APPLE__
#define _DARWIN_C_SOURCE
#endif

#include "pty.h"

#include <fcntl.h>    /* O_RDWR, O_NOCTTY */
#include <stdlib.h>   /* posix_openpt, grantpt, unlockpt, ptsname */
#include <string.h>   /* strncpy */
#include <termios.h>  /* struct termios, cfmakeraw, tcgetattr, tcsetattr */
#include <unistd.h>   /* close, write */

int pty_create(char *slave_path, size_t path_size)
{
    int master_fd = posix_openpt(O_RDWR | O_NOCTTY);
    if (master_fd < 0)
        return -1;

    if (grantpt(master_fd) < 0 || unlockpt(master_fd) < 0) {
        close(master_fd);
        return -1;
    }

    const char *name = ptsname(master_fd);
    if (!name) {
        close(master_fd);
        return -1;
    }

    strncpy(slave_path, name, path_size - 1);
    slave_path[path_size - 1] = '\0';

    /* Put the master side into raw mode so control characters are not
     * interpreted and NMEA sentences pass through unmodified. */
    struct termios tio;
    if (tcgetattr(master_fd, &tio) == 0) {
        cfmakeraw(&tio);
        tcsetattr(master_fd, TCSANOW, &tio);
    }

    return master_fd;
}

int pty_write(int master_fd, const char *data, int len)
{
    return (int)write(master_fd, data, (size_t)len);
}

void pty_close(int master_fd)
{
    close(master_fd);
}
