Written by DCMTK, an independent implementation, so the reader is tested against files it did not produce:

    dump2dcm +te source.dump native-explicit.dcm
    dump2dcm +ti source.dump native-implicit.dcm
    dcmcjpeg +e1 native-explicit.dcm jpeg-lossless.dcm

A 4x4, 8-bit, two-frame secondary capture with an undefined-length sequence. All names and identifiers are invented.
