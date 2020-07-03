import re
import datetime
from decimal import *


# TODO: Create classes for each bank

class Haspa():
    pass


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
    d = s.replace(",", ".")
    return float(d)


def rm_spaces(s):
    return " ".join(s.split())

def rm_quotes(s):
    return s.strip("\"")


# def booking_status(str):
#     if re.search(str, str)
#     return str