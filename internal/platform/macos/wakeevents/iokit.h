#ifndef SCHLAFLOS_IOKIT_H
#define SCHLAFLOS_IOKIT_H

char *schlaflos_list_events(void);
int schlaflos_schedule_event(double unix_seconds, const char *owner, const char *type);
int schlaflos_cancel_event(double unix_seconds, const char *owner, const char *type);

#endif
