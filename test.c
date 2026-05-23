#include <sys/stat.h>
#include <stdio.h>
#include <errno.h>

int main() {
    struct stat st;
    int r = stat("/this_does_not_exist", &st);
    printf("r = %d, errno = %d\n", r, errno);
    return 0;
}
