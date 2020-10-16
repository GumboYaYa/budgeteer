from apps.main.models import Account, Optionee, Transaction
from .forms import FileUploadForm
from .utils import cnvt_date, cnvt_float, rm_spaces, rm_quotes, cnvt_booking_type


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

        form = FileUploadForm(data_dict)

        if form.is_valid():
            print("Form is valid.")
            form.save()
            print("Form saved.")
        else:
            print("Form is not valid.")
        print(form.errors)


def do_dkb(data):
    new_iban = data[0][1].split(" / ")[0]
    for line in data[7:]:

        d_account = Account(
            iban=new_iban
        )
        d_account.save()

        d_optionee = Optionee(
            iban=line[5],
            bic=line[6],
            name=rm_spaces(line[3])
        )
        d_optionee.save()

        d_transaction = Transaction(
            account=d_account,
            date_booking=cnvt_date(line[0]),
            booking_type=cnvt_booking_type(line[2]),
            optionee=d_optionee,
            figure=cnvt_float(line[7]),
            currency="EUR",
            reference=rm_spaces(line[4])
        )
        d_transaction.save()

bank_templates = {
    "HAS": do_has,
    "DKB": do_dkb,
}

def run_template(id, data):
    return bank_templates[id](data)
