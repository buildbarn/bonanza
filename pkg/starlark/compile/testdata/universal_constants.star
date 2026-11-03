# None, True and False have instructions of their own, while other
# universal names are looked up by name. Names that shadow the constants
# should be left alone.

def constants():
    return [None, True, False, len]

def shadowed(None):
    return None
