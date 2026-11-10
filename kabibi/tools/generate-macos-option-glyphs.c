#include <Carbon/Carbon.h>
#include <stdio.h>

typedef struct {
    UInt16 key_code;
    char base_key;
} Key;

typedef struct {
    UInt16 glyph;
    char base_key;
} Mapping;

static const Key keys[] = {
    {18, '1'}, {19, '2'}, {20, '3'}, {21, '4'}, {23, '5'},
    {22, '6'}, {26, '7'}, {28, '8'}, {25, '9'}, {29, '0'},
    {12, 'q'}, {13, 'w'}, {14, 'e'}, {15, 'r'}, {17, 't'},
    {16, 'y'}, {32, 'u'}, {34, 'i'}, {31, 'o'}, {35, 'p'},
    {0, 'a'}, {1, 's'}, {2, 'd'}, {3, 'f'}, {5, 'g'},
    {4, 'h'}, {38, 'j'}, {40, 'k'}, {37, 'l'},
    {6, 'z'}, {7, 'x'}, {8, 'c'}, {9, 'v'}, {11, 'b'},
    {45, 'n'}, {46, 'm'},
};

int main(void) {
    TISInputSourceRef source = TISCopyCurrentKeyboardInputSource();
    if (source == NULL) {
        fputs("Unable to read the current keyboard input source.\n", stderr);
        return 1;
    }

    CFStringRef identifier = TISGetInputSourceProperty(source, kTISPropertyInputSourceID);
    CFDataRef layout_data = TISGetInputSourceProperty(source, kTISPropertyUnicodeKeyLayoutData);
    if (identifier == NULL || layout_data == NULL) {
        fputs("The active input source has no Unicode keyboard layout data.\n", stderr);
        CFRelease(source);
        return 1;
    }

    char layout_id[256];
    if (!CFStringGetCString(identifier, layout_id, sizeof(layout_id), kCFStringEncodingUTF8)) {
        fputs("Unable to encode the active input-source identifier.\n", stderr);
        CFRelease(source);
        return 1;
    }

    const UCKeyboardLayout *layout =
        (const UCKeyboardLayout *)CFDataGetBytePtr(layout_data);
    if (layout == NULL) {
        fputs("The active input source has no keyboard layout bytes.\n", stderr);
        CFRelease(source);
        return 1;
    }

    Mapping mappings[sizeof(keys) / sizeof(keys[0])];
    size_t mapping_count = 0;
    for (size_t i = 0; i < sizeof(keys) / sizeof(keys[0]); ++i) {
        UInt32 dead_key_state = 0;
        UniChar characters[4];
        UniCharCount length = 0;
        OSStatus status = UCKeyTranslate(
            layout,
            keys[i].key_code,
            kUCKeyActionDown,
            optionKey >> 8,
            LMGetKbdType(),
            0,
            &dead_key_state,
            sizeof(characters) / sizeof(characters[0]),
            &length,
            characters);
        if (status != noErr || dead_key_state != 0 || length != 1 ||
            characters[0] <= 0x7f) {
            continue;
        }
        mappings[mapping_count++] = (Mapping){characters[0], keys[i].base_key};
    }

    printf("// Active input source: %s\n", layout_id);
    printf("// Unique printable Option+letter/digit glyphs: ");
    size_t unique_count = 0;
    for (size_t i = 0; i < mapping_count; ++i) {
        size_t occurrences = 0;
        for (size_t j = 0; j < mapping_count; ++j) {
            if (mappings[j].glyph == mappings[i].glyph) {
                ++occurrences;
            }
        }
        if (occurrences == 1) {
            ++unique_count;
        }
    }
    printf("%zu\n", unique_count);

    for (size_t i = 0; i < mapping_count; ++i) {
        size_t occurrences = 0;
        for (size_t j = 0; j < mapping_count; ++j) {
            if (mappings[j].glyph == mappings[i].glyph) {
                ++occurrences;
            }
        }
        if (occurrences == 1) {
            printf("\t'\\u%04x': '%c',\n", mappings[i].glyph, mappings[i].base_key);
        }
    }

    CFRelease(source);
    return 0;
}
