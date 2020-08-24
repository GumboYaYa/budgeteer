import re
import datetime
from decimal import *


def cnvt_csv(file):
        data = file.replace("\"", "")
        lines = data.splitlines()
        return [x.split(";") for x in lines]


def cnvt_date(s):
    date_split = s.split(".")

    # Haspa
    if len(date_split[2]) == 2:
        date_split[2] = f"20{date_split[2]}"

    # Convert str to int
    date_split = [int(x) for x in date_split]

    # Unpack list
    d, m, y = date_split

    return datetime.date(y, m, d)


def cnvt_float(s):
    d = s.replace(".", "").replace(",", ".")
    return float(d)


def rm_spaces(s):
    return " ".join(s.split())


def rm_quotes(s):
    return s.strip("\"")


def cnvt_booking_type(s, *args):
    if re.search("kartenzahl", s, flags=re.IGNORECASE):
        return "Kartenzahlung"
    elif re.search("lastsch", s, flags=re.IGNORECASE):
        return "Lastschrift"
    elif re.search("dauerauf", s, flags=re.IGNORECASE):
        return "Dauerauftrag"
    elif re.search("gutsch", s, flags=re.IGNORECASE):
        return "Gutschrift"
    elif re.search("kreditk", s, flags=re.IGNORECASE):
        return "Kreditkarte"
    elif re.search("lohn|gehalt|rente", s, flags=re.IGNORECASE):
        return "Lohn"
    elif re.search("barge", s, flags=re.IGNORECASE):
        return "Bargeldauszahlung"
    elif re.search("abschl", s, flags=re.IGNORECASE):
        return "Abschluss"
    elif re.search("berweisung", s, flags=re.IGNORECASE):
        return "Überweisung"
    elif s == "":
        for arg in args:
            if re.search("kreditk", arg, flags=re.IGNORECASE):
                return "Kreditkarte"
    else:
        return s
