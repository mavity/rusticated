#include <stdio.h>
#include <errno.h>

struct stat;
int stat(const char*, struct stat*);

int main() {
    int r = stat("/this_does_not_exist", NULL);
    printf("r = %d, errno = %d\n", r, errno);
    return 0;
}
