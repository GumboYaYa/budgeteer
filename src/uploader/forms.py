from django import forms
from .models import Account, Currency, Optionee, OptioneeId, Transaction


class FileUploadForm(forms.Form):

    file = forms.FileField()

    def save(self):
        data = self.cleaned_data

        acc, created = Account.objects.get_or_create(iban=data["iban"])
        if created:
            acc.save()

        curr, created = Currency.objects.get_or_create(currency_short=data["currency"])
        if created:
            curr.save()

        optid, created = OptioneeId.objects.get_or_create(optionee_id=data["optionee_id"])
        if created:
            curr.save()

        opt, created = Optionee.objects.get_or_create(name=data["optionee_name"], iban=data["optionee_iban"], bic=data["optionee_bic"])
        if created:
            curr.save()

        transaction = Transaction.objects.create(
            account=acc,
            date_booking=data["date_booking"],
            optionee_id=optid,
            optionee=opt,
            figure=data["figure"],
            currency=curr,
            reference=data["reference"],
        )
        transaction.save()
