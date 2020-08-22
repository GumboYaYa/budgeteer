from .forms import FileUploadForm
from .utils import cnvt_date, cnvt_float, rm_spaces, rm_quotes, cnvt_booking_type


def save_form(data, item):
    form = FileUploadForm(data)

    if form.is_valid():
        print("Form is valid.")
        form.save()
        print("Form saved.")
    else:
        print("Form is not valid:")
        print(item)
    print(form.errors)


def do_has(data):
    for line in data[1:]:
        data_dict = {}
        data_dict["iban"] = line[0]
        data_dict["date_booking"] = cnvt_date(line[1])
        data_dict["booking_type"] = cnvt_booking_type(line[3])
        data_dict["reference"] = rm_spaces(line[4])
        data_dict["optionee_name"] = rm_spaces(line[11])
        data_dict["optionee_iban"] = line[12]
        data_dict["optionee_bic"] = line[13]
        data_dict["figure"] = cnvt_float(line[14])
        data_dict["currency"] = line[15]
        # print(data_dict)

        save_form(data_dict, line)


def do_dkb(data):
    iban = data[0][1].split(" / ")[0]
    for line in data[7:]:
        data_dict = {}
        data_dict["iban"] = iban
        data_dict["date_booking"] = cnvt_date(line[0])
        data_dict["booking_type"] = cnvt_booking_type(line[2], line[3])
        data_dict["reference"] = rm_spaces(line[4])
        data_dict["optionee_name"] = rm_spaces(line[3])
        data_dict["optionee_iban"] = line[5]
        data_dict["optionee_bic"] = line[6]
        data_dict["figure"] = cnvt_float(line[7])
        data_dict["currency"] = "EUR"
        # print(data_dict)
        
        save_form(data_dict, line)


bank_templates = {
    "HAS": do_has,
    "DKB": do_dkb,
}

def run_template(id, data):
    return bank_templates[id](data)
